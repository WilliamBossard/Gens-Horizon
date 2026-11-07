package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
)

type DropboxProvider struct {
	tokenData auth.TokenData
	creds     config.ProviderConfig
	tokenPath string
	client    *http.Client
	mu        sync.Mutex
}

func NewDropboxProvider(tokenData auth.TokenData, creds config.ProviderConfig, tokenPath string) *DropboxProvider {
	return &DropboxProvider{
		tokenData: tokenData,
		creds:     creds,
		tokenPath: tokenPath,
		client:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (d *DropboxProvider) Name() string {
	return "dropbox"
}

func (d *DropboxProvider) getAccessToken() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.tokenData.AccessToken == "" || (d.tokenData.ExpiryDate > 0 && time.Now().UnixMilli() > (d.tokenData.ExpiryDate-60000)) {
		if d.tokenData.RefreshToken != "" {
			if err := d.refreshToken(); err != nil {
				return "", err
			}
		}
	}
	return d.tokenData.AccessToken, nil
}

func (d *DropboxProvider) refreshToken() error {
	form := url.Values{
		"client_id":     {d.creds.ClientID},
		"client_secret": {d.creds.ClientSecret},
		"refresh_token": {d.tokenData.RefreshToken},
		"grant_type":    {"refresh_token"},
	}

	resp, err := http.PostForm("https://api.dropboxapi.com/oauth2/token", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("erreur refresh token Dropbox (HTTP %d) : %s", resp.StatusCode, string(bodyBytes))
	}

	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return err
	}

	d.tokenData.AccessToken = res.AccessToken
	if res.ExpiresIn > 0 {
		d.tokenData.ExpiryDate = time.Now().UnixMilli() + (res.ExpiresIn * 1000)
	}

	if err := auth.EncryptToken(d.tokenPath, d.tokenData); err != nil {
		ipc.Logf("[dropbox] Échec sauvegarde token rafraîchi : %v", err)
	}

	return nil
}

func (d *DropboxProvider) doRPC(endpoint string, reqBody any) ([]byte, error) {
	tok, err := d.getAccessToken()
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.dropboxapi.com/2/"+endpoint, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		d.mu.Lock()
		_ = d.refreshToken()
		d.mu.Unlock()
		newTok, _ := d.getAccessToken()
		req.Header.Set("Authorization", "Bearer "+newTok)
		if reqBody != nil {
			b, _ := json.Marshal(reqBody)
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
		resp2, err := d.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		respBytes, _ = io.ReadAll(resp2.Body)
		if resp2.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("dropbox RPC %s HTTP %d : %s", endpoint, resp2.StatusCode, string(respBytes))
		}
		return respBytes, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dropbox RPC %s HTTP %d : %s", endpoint, resp.StatusCode, string(respBytes))
	}
	return respBytes, nil
}

func (d *DropboxProvider) ListFiles(nameContains string) ([]CloudFile, error) {
	var files []CloudFile

	body := map[string]any{
		"path":                                "",
		"recursive":                           false,
		"include_media_info":                  false,
		"include_deleted":                     false,
		"include_has_explicit_shared_members": false,
		"include_mounted_folders":             false,
		"limit":                               1000,
	}

	respBytes, err := d.doRPC("files/list_folder", body)
	if err != nil {
		return nil, err
	}

	type Entry struct {
		Tag            string `json:".tag"`
		ID             string `json:"id"`
		Name           string `json:"name"`
		ServerModified string `json:"server_modified"`
		Size           int64  `json:"size"`
	}
	var res struct {
		Entries []Entry `json:"entries"`
		Cursor  string  `json:"cursor"`
		HasMore bool    `json:"has_more"`
	}
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return nil, err
	}

	processEntries := func(entries []Entry) {
		for _, e := range entries {
			if e.Tag == "file" {
				if nameContains == "" || strings.Contains(e.Name, nameContains) {
					t, _ := time.Parse(time.RFC3339, e.ServerModified)
					files = append(files, CloudFile{
						ID:           e.ID,
						Name:         e.Name,
						ModifiedTime: t,
						Size:         e.Size,
					})
				}
			}
		}
	}

	processEntries(res.Entries)

	for res.HasMore && res.Cursor != "" {
		contBytes, err := d.doRPC("files/list_folder/continue", map[string]any{"cursor": res.Cursor})
		if err != nil {
			break
		}
		if err := json.Unmarshal(contBytes, &res); err != nil {
			break
		}
		processEntries(res.Entries)
	}

	return files, nil
}

func (d *DropboxProvider) DownloadFile(fileID string, destPath string, onProgress func(pct int), totalSize int64) error {
	dlClient := &http.Client{Timeout: 10 * time.Minute}
	arg, _ := json.Marshal(map[string]string{"path": fileID})

	return downloadWithResume(destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
		tok, err := d.getAccessToken()
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequest(http.MethodPost, "https://content.dropboxapi.com/2/files/download", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Dropbox-API-Arg", string(arg))
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		return dlClient.Do(req)
	})
}

func (d *DropboxProvider) DownloadJSON(fileID string, target any) error {
	tok, err := d.getAccessToken()
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://content.dropboxapi.com/2/files/download", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	arg, _ := json.Marshal(map[string]string{"path": fileID})
	req.Header.Set("Dropbox-API-Arg", string(arg))

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dropbox downloadJSON HTTP %d : %s", resp.StatusCode, string(b))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

func (d *DropboxProvider) UploadZip(name string, srcPath string, existingID string, onProgress func(pct int)) (string, error) {
	tok, err := d.getAccessToken()
	if err != nil {
		return "", err
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	fileSize := info.Size()

	targetPath := "/" + name
	if existingID != "" && strings.HasPrefix(existingID, "id:") {
		targetPath = existingID
	}

	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	const dropboxChunkSize = int64(8 * 1024 * 1024)
	uploadClient := &http.Client{Timeout: 5 * time.Minute}
	var processed int64
	lastPct := -1

	// Pour les petits fichiers (<= 8 Mo) : upload direct avec ProgressReader
	if fileSize <= dropboxChunkSize {
		pr := &ProgressReader{
			Reader:     file,
			TotalSize:  fileSize,
			Processed:  &processed,
			LastPct:    &lastPct,
			OnProgress: onProgress,
		}

		req, err := http.NewRequest(http.MethodPost, "https://content.dropboxapi.com/2/files/upload", pr)
		if err != nil {
			return "", err
		}
		req.ContentLength = fileSize
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/octet-stream")

		arg, _ := json.Marshal(map[string]any{
			"path":       targetPath,
			"mode":       "overwrite",
			"autorename": false,
			"mute":       true,
		})
		req.Header.Set("Dropbox-API-Arg", string(arg))

		resp, err := uploadClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		respBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("dropbox uploadZip HTTP %d : %s", resp.StatusCode, string(respBytes))
		}

		var res struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBytes, &res); err != nil {
			return "", err
		}

		InvalidateCloudIndexCache()
		if onProgress != nil {
			onProgress(100)
		}
		return res.ID, nil
	}

	// Pour les fichiers > 8 Mo : session d'upload par blocs (upload_session/start, append_v2, finish)
	firstChunk := dropboxChunkSize
	var sessionID string
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*1500) * time.Millisecond)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		processed = 0

		pr := &ProgressReader{
			Reader:     io.LimitReader(file, firstChunk),
			TotalSize:  fileSize,
			Processed:  &processed,
			LastPct:    &lastPct,
			OnProgress: onProgress,
		}

		startReq, err := http.NewRequest(http.MethodPost, "https://content.dropboxapi.com/2/files/upload_session/start", pr)
		if err != nil {
			return "", err
		}
		startReq.ContentLength = firstChunk
		startReq.Header.Set("Authorization", "Bearer "+tok)
		startReq.Header.Set("Content-Type", "application/octet-stream")
		startArg, _ := json.Marshal(map[string]any{"close": false})
		startReq.Header.Set("Dropbox-API-Arg", string(startArg))

		resp, err := uploadClient.Do(startReq)
		if err != nil {
			if attempt == 2 {
				return "", err
			}
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			if attempt == 2 {
				return "", fmt.Errorf("dropbox session start HTTP %d : %s", resp.StatusCode, string(b))
			}
			continue
		}

		var sRes struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(b, &sRes); err != nil || sRes.SessionID == "" {
			return "", fmt.Errorf("dropbox session_id invalide : %v", err)
		}
		sessionID = sRes.SessionID
		break
	}

	offset := firstChunk
	for offset+dropboxChunkSize < fileSize {
		chunkLen := dropboxChunkSize
		var appended bool
		var lastErr error

		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*1500) * time.Millisecond)
			}
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				return "", err
			}
			processed = offset

			pr := &ProgressReader{
				Reader:     io.LimitReader(file, chunkLen),
				TotalSize:  fileSize,
				Processed:  &processed,
				LastPct:    &lastPct,
				OnProgress: onProgress,
			}

			appReq, err := http.NewRequest(http.MethodPost, "https://content.dropboxapi.com/2/files/upload_session/append_v2", pr)
			if err != nil {
				return "", err
			}
			appReq.ContentLength = chunkLen
			appReq.Header.Set("Authorization", "Bearer "+tok)
			appReq.Header.Set("Content-Type", "application/octet-stream")
			appArg, _ := json.Marshal(map[string]any{
				"cursor": map[string]any{
					"session_id": sessionID,
					"offset":     offset,
				},
				"close": false,
			})
			appReq.Header.Set("Dropbox-API-Arg", string(appArg))

			resp, err := uploadClient.Do(appReq)
			if err != nil {
				lastErr = err
				continue
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				appended = true
				break
			}
			lastErr = fmt.Errorf("dropbox append_v2 HTTP %d : %s", resp.StatusCode, string(b))
		}
		if !appended {
			return "", lastErr
		}
		offset += chunkLen
	}

	// Dernier bloc : upload_session/finish
	finalLen := fileSize - offset
	var finalID string
	var finishErr error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*1500) * time.Millisecond)
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return "", err
		}
		processed = offset

		pr := &ProgressReader{
			Reader:     io.LimitReader(file, finalLen),
			TotalSize:  fileSize,
			Processed:  &processed,
			LastPct:    &lastPct,
			OnProgress: onProgress,
		}

		finReq, err := http.NewRequest(http.MethodPost, "https://content.dropboxapi.com/2/files/upload_session/finish", pr)
		if err != nil {
			return "", err
		}
		finReq.ContentLength = finalLen
		finReq.Header.Set("Authorization", "Bearer "+tok)
		finReq.Header.Set("Content-Type", "application/octet-stream")
		finArg, _ := json.Marshal(map[string]any{
			"cursor": map[string]any{
				"session_id": sessionID,
				"offset":     offset,
			},
			"commit": map[string]any{
				"path":       targetPath,
				"mode":       "overwrite",
				"autorename": false,
				"mute":       true,
			},
		})
		finReq.Header.Set("Dropbox-API-Arg", string(finArg))

		resp, err := uploadClient.Do(finReq)
		if err != nil {
			finishErr = err
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var res struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(b, &res); err != nil {
				return "", err
			}
			finalID = res.ID
			finishErr = nil
			break
		}
		finishErr = fmt.Errorf("dropbox finish HTTP %d : %s", resp.StatusCode, string(b))
	}

	if finishErr != nil {
		return "", finishErr
	}

	InvalidateCloudIndexCache()
	if onProgress != nil {
		onProgress(100)
	}
	return finalID, nil
}

func (d *DropboxProvider) UploadJSON(name string, data any, existingID string) (string, error) {
	tok, err := d.getAccessToken()
	if err != nil {
		return "", err
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	targetPath := "/" + name
	if existingID != "" && strings.HasPrefix(existingID, "id:") {
		targetPath = existingID
	}

	req, err := http.NewRequest(http.MethodPost, "https://content.dropboxapi.com/2/files/upload", bytes.NewReader(jsonBytes))
	if err != nil {
		return "", err
	}
	req.ContentLength = int64(len(jsonBytes))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/octet-stream")

	arg, _ := json.Marshal(map[string]any{
		"path":       targetPath,
		"mode":       "overwrite",
		"autorename": false,
		"mute":       true,
	})
	req.Header.Set("Dropbox-API-Arg", string(arg))

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dropbox uploadJSON HTTP %d : %s", resp.StatusCode, string(respBytes))
	}

	var res struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(respBytes, &res)
	InvalidateCloudIndexCache()
	return res.ID, nil
}

func (d *DropboxProvider) DeleteFile(fileID string) error {
	_, err := d.doRPC("files/delete_v2", map[string]string{"path": fileID})
	if err == nil {
		InvalidateCloudIndexCache()
	}
	return err
}

func (d *DropboxProvider) GetQuota() (int64, int64, error) {
	respBytes, err := d.doRPC("users/get_space_usage", nil)
	if err != nil {
		return 0, 0, err
	}

	var res struct {
		Used       int64 `json:"used"`
		Allocation struct {
			Allocated int64 `json:"allocated"`
		} `json:"allocation"`
	}
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return 0, 0, err
	}

	return res.Used, res.Allocation.Allocated, nil
}
