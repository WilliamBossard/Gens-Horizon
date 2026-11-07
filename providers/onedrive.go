package providers

import (
	"bytes"
	"encoding/json"
	"errors"
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

type OneDriveProvider struct {
	tokenData auth.TokenData
	creds     config.ProviderConfig
	tokenPath string
	client    *http.Client
	mu        sync.Mutex
}

func NewOneDriveProvider(tokenData auth.TokenData, creds config.ProviderConfig, tokenPath string) *OneDriveProvider {
	return &OneDriveProvider{
		tokenData: tokenData,
		creds:     creds,
		tokenPath: tokenPath,
		client:    NewPooledClient(60 * time.Second),
	}
}

func (o *OneDriveProvider) Name() string {
	return "onedrive"
}

func (o *OneDriveProvider) getAccessToken() (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.tokenData.AccessToken == "" || (o.tokenData.ExpiryDate > 0 && time.Now().UnixMilli() > (o.tokenData.ExpiryDate-60000)) {
		if o.tokenData.RefreshToken != "" {
			if err := o.refreshToken(); err != nil {
				return "", err
			}
		}
	}
	return o.tokenData.AccessToken, nil
}

func (o *OneDriveProvider) refreshToken() error {
	scope := o.creds.Scope
	if scope == "" {
		scope = "Files.ReadWrite offline_access User.Read"
	}

	form := url.Values{
		"client_id":     {o.creds.ClientID},
		"refresh_token": {o.tokenData.RefreshToken},
		"grant_type":    {"refresh_token"},
		"scope":         {scope},
	}
	if o.creds.ClientSecret != "" {
		form.Set("client_secret", o.creds.ClientSecret)
	}

	resp, err := http.PostForm("https://login.microsoftonline.com/common/oauth2/v2.0/token", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("erreur refresh token OneDrive (HTTP %d) : %s", resp.StatusCode, string(bodyBytes))
	}

	var res struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return err
	}

	o.tokenData.AccessToken = res.AccessToken
	if res.RefreshToken != "" {
		o.tokenData.RefreshToken = res.RefreshToken
	}
	if res.ExpiresIn > 0 {
		o.tokenData.ExpiryDate = time.Now().UnixMilli() + (res.ExpiresIn * 1000)
	}
	if res.TokenType != "" {
		o.tokenData.TokenType = res.TokenType
	}

	if err := auth.EncryptToken(o.tokenPath, o.tokenData); err != nil {
		ipc.Logf("[onedrive] Échec sauvegarde token rafraîchi : %v", err)
	}

	return nil
}

func (o *OneDriveProvider) doRequest(req *http.Request) (*http.Response, error) {
	tok, err := o.getAccessToken()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		o.mu.Lock()
		_ = o.refreshToken()
		o.mu.Unlock()
		newTok, _ := o.getAccessToken()
		req.Header.Set("Authorization", "Bearer "+newTok)
		return o.client.Do(req)
	}

	return resp, nil
}

func (o *OneDriveProvider) ListFiles(nameContains string) ([]CloudFile, error) {
	var files []CloudFile
	nextURL := "https://graph.microsoft.com/v1.0/me/drive/special/approot/children?$top=1000"

	for nextURL != "" {
		req, err := http.NewRequest(http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := o.doRequest(req)
		if err != nil {
			return nil, err
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("onedrive listFiles HTTP %d : %s", resp.StatusCode, string(bodyBytes))
		}

		type Item struct {
			ID                   string `json:"id"`
			Name                 string `json:"name"`
			LastModifiedDateTime string `json:"lastModifiedDateTime"`
			Size                 int64  `json:"size"`
			File                 any    `json:"file"`
		}
		var res struct {
			Value    []Item `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(bodyBytes, &res); err != nil {
			return nil, err
		}

		for _, item := range res.Value {
			if item.File != nil {
				if nameContains == "" || strings.Contains(item.Name, nameContains) {
					t, _ := time.Parse(time.RFC3339, item.LastModifiedDateTime)
					files = append(files, CloudFile{
						ID:           item.ID,
						Name:         item.Name,
						ModifiedTime: t,
						Size:         item.Size,
					})
				}
			}
		}

		nextURL = res.NextLink
	}

	return files, nil
}

func (o *OneDriveProvider) DownloadFile(fileID string, destPath string, onProgress func(pct int), totalSize int64) error {
	dlClient := NewPooledClient(10 * time.Minute)
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/content", fileID)

	return downloadWithResume(destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
		tok, err := o.getAccessToken()
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequest(http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		return dlClient.Do(req)
	})
}

func (o *OneDriveProvider) DownloadJSON(fileID string, target any) error {
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/content", fileID)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := o.doRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("onedrive downloadJSON HTTP %d : %s", resp.StatusCode, string(b))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

func (o *OneDriveProvider) UploadZip(name string, srcPath string, existingID string, onProgress func(pct int)) (string, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	fileSize := info.Size()

	var processed int64
	lastPct := -1

	// Pour < 4 Mo : upload direct PUT simple avec ProgressReader
	if fileSize < 4*1024*1024 {
		file, err := os.Open(srcPath)
		if err != nil {
			return "", err
		}
		defer file.Close()

		pr := &ProgressReader{
			Reader:     file,
			TotalSize:  fileSize,
			Processed:  &processed,
			LastPct:    &lastPct,
			OnProgress: onProgress,
		}

		reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/special/approot:/%s:/content", url.PathEscape(name))
		req, err := http.NewRequest(http.MethodPut, reqURL, pr)
		if err != nil {
			return "", err
		}
		req.ContentLength = fileSize
		req.Header.Set("Content-Type", "application/zip")

		resp, err := o.doRequest(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return "", fmt.Errorf("onedrive simple upload HTTP %d : %s", resp.StatusCode, string(b))
		}
		var res struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(b, &res)
		InvalidateCloudIndexCache()
		if onProgress != nil {
			onProgress(100)
		}
		return res.ID, nil
	}

	// Pour >= 4 Mo : createUploadSession
	sessionURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/special/approot:/%s:/createUploadSession", url.PathEscape(name))
	sessBody, _ := json.Marshal(map[string]any{
		"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace", "name": name},
	})
	req, err := http.NewRequest(http.MethodPost, sessionURL, bytes.NewReader(sessBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.doRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	sessBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("onedrive createUploadSession HTTP %d : %s", resp.StatusCode, string(sessBytes))
	}

	var sessionRes struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(sessBytes, &sessionRes); err != nil || sessionRes.UploadURL == "" {
		return "", errors.New("aucune URL d'upload session retournée par OneDrive")
	}

	// Upload par chunks de 10 Mo avec ProgressReader et reprise
	chunkSize := int64(10 * 1024 * 1024)
	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	uploadClient := NewPooledClient(5 * time.Minute)
	var offset int64
	var finalID string

	for offset < fileSize {
		remaining := fileSize - offset
		currentChunk := chunkSize
		if remaining < currentChunk {
			currentChunk = remaining
		}

		var chunkDone bool
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
				Reader:     io.LimitReader(file, currentChunk),
				TotalSize:  fileSize,
				Processed:  &processed,
				LastPct:    &lastPct,
				OnProgress: onProgress,
			}

			chunkReq, err := http.NewRequest(http.MethodPut, sessionRes.UploadURL, pr)
			if err != nil {
				return "", err
			}
			chunkReq.ContentLength = currentChunk
			chunkReq.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+currentChunk-1, fileSize))

			chunkResp, err := uploadClient.Do(chunkReq)
			if err != nil {
				lastErr = err
				continue
			}
			chunkBody, _ := io.ReadAll(chunkResp.Body)
			_ = chunkResp.Body.Close()

			if chunkResp.StatusCode != http.StatusOK && chunkResp.StatusCode != http.StatusCreated && chunkResp.StatusCode != http.StatusAccepted {
				lastErr = fmt.Errorf("onedrive chunk upload HTTP %d : %s", chunkResp.StatusCode, string(chunkBody))
				continue
			}

			if chunkResp.StatusCode == http.StatusOK || chunkResp.StatusCode == http.StatusCreated {
				var doneRes struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(chunkBody, &doneRes)
				finalID = doneRes.ID
			}
			chunkDone = true
			break
		}

		if !chunkDone {
			return "", lastErr
		}
		offset += currentChunk
	}

	InvalidateCloudIndexCache()
	if onProgress != nil {
		onProgress(100)
	}
	return finalID, nil
}

func (o *OneDriveProvider) UploadJSON(name string, data any, existingID string) (string, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/special/approot:/%s:/content", url.PathEscape(name))
	req, err := http.NewRequest(http.MethodPut, reqURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return "", err
	}
	req.ContentLength = int64(len(jsonBytes))
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.doRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("onedrive uploadJSON HTTP %d : %s", resp.StatusCode, string(b))
	}

	var res struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &res)
	InvalidateCloudIndexCache()
	return res.ID, nil
}

func (o *OneDriveProvider) DeleteFile(fileID string) error {
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s", fileID)
	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := o.doRequest(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("onedrive delete HTTP %d", resp.StatusCode)
	}
	InvalidateCloudIndexCache()
	return nil
}

func (o *OneDriveProvider) GetQuota() (int64, int64, error) {
	reqURL := "https://graph.microsoft.com/v1.0/me/drive"
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, 0, err
	}

	resp, err := o.doRequest(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("onedrive quota HTTP %d : %s", resp.StatusCode, string(b))
	}

	var res struct {
		Quota struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return 0, 0, err
	}

	return res.Quota.Used, res.Quota.Total, nil
}

