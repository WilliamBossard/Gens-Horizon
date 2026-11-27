package providers

import (
	"bytes"
	"context"
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
	"unicode/utf16"

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
		client:    NewPooledClient(60 * time.Second),
	}
}

func (d *DropboxProvider) Name() string {
	return "dropbox"
}

func (d *DropboxProvider) getAccessToken() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.tokenData.AccessToken == "" || ((d.tokenData.ExpiryDate == 0 || time.Now().UnixMilli() > (d.tokenData.ExpiryDate-60000)) && d.tokenData.RefreshToken != "") {
		if d.tokenData.RefreshToken != "" {
			if err := d.refreshToken(); err != nil {
				return "", err
			}
		}
	}
	if d.tokenData.AccessToken == "" && d.tokenData.RefreshToken == "" {
		return "", errors.New("[dropbox] aucun token d'accès ou de rafraîchissement disponible")
	}
	return d.tokenData.AccessToken, nil
}

func (d *DropboxProvider) refreshToken() error {
	form := url.Values{
		"client_id":     {d.creds.ClientID},
		"refresh_token": {d.tokenData.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	if d.creds.ClientSecret != "" {
		form.Set("client_secret", d.creds.ClientSecret)
	}

	req, err := http.NewRequestWithContext(baseContext(), "POST", "https://api.dropboxapi.com/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return newProviderHTTPError("erreur refresh token Dropbox", resp, bodyBytes)
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

func (d *DropboxProvider) refreshTokenIfStale(failedToken string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.tokenData.AccessToken != "" && d.tokenData.AccessToken != failedToken {
		if d.tokenData.AccessToken == "" && d.tokenData.RefreshToken == "" {
		return "", errors.New("[dropbox] aucun token d'accès ou de rafraîchissement disponible")
	}
	return d.tokenData.AccessToken, nil
	}
	if d.tokenData.RefreshToken == "" {
		return "", errors.New("dropbox: aucun refresh_token disponible")
	}
	if err := d.refreshToken(); err != nil {
		return "", err
	}
	if d.tokenData.AccessToken == "" && d.tokenData.RefreshToken == "" {
		return "", errors.New("[dropbox] aucun token d'accès ou de rafraîchissement disponible")
	}
	return d.tokenData.AccessToken, nil
}

func (d *DropboxProvider) doRPC(ctx context.Context, endpoint string, reqBody any) ([]byte, error) {
	if ctx == nil {
		ctx = baseContext()
	}
	tok, err := d.getAccessToken()
	if err != nil {
		return nil, err
	}

	var bodyBytes []byte
	if reqBody != nil {
		bodyBytes, err = json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
	}

	buildReq := func(token string) (*http.Request, error) {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.dropboxapi.com/2/"+endpoint, bodyReader)
		if rErr != nil {
			return nil, rErr
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}

	req, err := buildReq(tok)
	if err != nil {
		return nil, err
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		newTok, rErr := d.refreshTokenIfStale(tok)
		if rErr != nil {
			return nil, rErr
		}
		req2, err := buildReq(newTok)
		if err != nil {
			return nil, err
		}
		resp2, err := d.client.Do(req2)
		if err != nil {
			return nil, err
		}
		respBytes, _ = io.ReadAll(io.LimitReader(resp2.Body, 16*1024*1024))
		_ = resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			return nil, newProviderHTTPError("dropbox RPC "+endpoint, resp2, respBytes)
		}
		return respBytes, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, newProviderHTTPError("dropbox RPC "+endpoint, resp, respBytes)
	}
	return respBytes, nil
}

func (d *DropboxProvider) ListFiles(ctx context.Context, nameContains string) ([]CloudFile, error) {
	if ctx == nil {
		ctx = baseContext()
	}
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

	respBytes, err := d.doRPC(ctx, "files/list_folder", body)
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

	const maxPages = 100
	for page := 1; res.HasMore && res.Cursor != "" && page < maxPages; page++ {
		contBytes, err := d.doRPC(ctx, "files/list_folder/continue", map[string]any{"cursor": res.Cursor})
		if err != nil {
			return nil, fmt.Errorf("dropbox list_folder/continue : %w", err)
		}
		if err := json.Unmarshal(contBytes, &res); err != nil {
			return nil, fmt.Errorf("dropbox list_folder/continue decode : %w", err)
		}
		processEntries(res.Entries)
	}

	if res.HasMore {
		return nil, fmt.Errorf("%w : dropbox list_folder exceeded %d pages", ErrListingTruncated, maxPages)
	}

	return files, nil
}

// marshalDropboxAPIArg sérialise v en JSON ASCII strict (\uXXXX) requis par l'en-tête HTTP Dropbox-API-Arg.
func marshalDropboxAPIArg(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range string(raw) {
		if r < 0x80 {
			b.WriteRune(r)
		} else {
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, "\\u%04x", u)
			}
		}
	}
	return b.String(), nil
}

func (d *DropboxProvider) buildDownloadRequest(ctx context.Context, token string, argJSON string, start, end int64) (*http.Request, error) {
	if ctx == nil {
		ctx = baseContext()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://content.dropboxapi.com/2/files/download", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Dropbox-API-Arg", argJSON)
	if end >= start && end >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	} else if start > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	}
	return req, nil
}

func (d *DropboxProvider) DownloadFile(ctx context.Context, fileID string, destPath string, onProgress func(pct int), totalSize int64) error {
	if ctx == nil {
		ctx = baseContext()
	}
	dlClient := streamingClient(d.client)
	arg, err := marshalDropboxAPIArg(map[string]string{"path": fileID})
	if err != nil {
		return err
	}

	return downloadSmartWithContext(ctx, destPath, totalSize, onProgress, func(start, end int64) (*http.Response, error) {
		tok, err := d.getAccessToken()
		if err != nil {
			return nil, err
		}

		req, err := d.buildDownloadRequest(ctx, tok, arg, start, end)
		if err != nil {
			return nil, err
		}

		resp, err := dlClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			newTok, rErr := d.refreshTokenIfStale(tok)
			if rErr != nil {
				return nil, rErr
			}
			req, err = d.buildDownloadRequest(ctx, newTok, arg, start, end)
			if err != nil {
				return nil, err
			}
			resp, err = dlClient.Do(req)
			if err != nil {
				return nil, err
			}
		}

		return resp, nil
	})
}

func (d *DropboxProvider) DownloadJSON(ctx context.Context, fileID string, target any) error {
	if ctx == nil {
		ctx = baseContext()
	}
	tok, err := d.getAccessToken()
	if err != nil {
		return err
	}
	arg, err := marshalDropboxAPIArg(map[string]string{"path": fileID})
	if err != nil {
		return err
	}

	req, err := d.buildDownloadRequest(ctx, tok, arg, 0, -1)
	if err != nil {
		return err
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		newTok, rErr := d.refreshTokenIfStale(tok)
		if rErr != nil {
			return rErr
		}
		req, err = d.buildDownloadRequest(ctx, newTok, arg, 0, -1)
		if err != nil {
			return err
		}
		resp, err = d.client.Do(req)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return newProviderHTTPError("dropbox downloadJSON", resp, b)
	}

	return json.NewDecoder(io.LimitReader(resp.Body, 16*1024*1024)).Decode(target)
}

var dropboxChunkSize = int64(8 * 1024 * 1024)

func (d *DropboxProvider) UploadZip(ctx context.Context, name string, srcPath string, existingID string, onProgress func(pct int)) (string, error) {
	if ctx == nil {
		ctx = baseContext()
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

	uploadClient := clientWithTimeout(d.client, 5*time.Minute)
	var processed int64
	lastPct := -1

	uploadSegment := func(endpoint string, argPayload any, offset, chunkLen int64) ([]byte, error) {
		argJSON, mErr := marshalDropboxAPIArg(argPayload)
		if mErr != nil {
			return nil, mErr
		}

		maxAttempts := effectiveMaxRetries()
		var lastErr error
		authRefreshed := false
		didRefreshThisIter := false

		for attempt := 0; attempt < maxAttempts; {
			if attempt > 0 && !didRefreshThisIter {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt*1500) * time.Millisecond):
				}
			}
			didRefreshThisIter = false

			tok, gErr := d.getAccessToken()
			if gErr != nil {
				return nil, gErr
			}

			if _, sErr := file.Seek(offset, io.SeekStart); sErr != nil {
				return nil, sErr
			}
			processed = offset

			pr := &ProgressReader{
				Reader:     io.LimitReader(file, chunkLen),
				TotalSize:  fileSize,
				Processed:  &processed,
				LastPct:    &lastPct,
				OnProgress: onProgress,
			}

			resp, cleanup, doErr := doUploadWithIdleTimeoutWithContext(ctx, uploadClient, http.MethodPost, endpoint, pr, chunkLen, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+tok)
				r.Header.Set("Content-Type", "application/octet-stream")
				r.Header.Set("Dropbox-API-Arg", argJSON)
			})
			if doErr != nil {
				lastErr = doErr
				attempt++
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
			_ = resp.Body.Close()
			cleanup()

			if resp.StatusCode == http.StatusOK {
				return b, nil
			}
			if resp.StatusCode == http.StatusConflict {
				var conflictBody struct {
					Error struct {
						Tag           string `json:".tag"`
						CorrectOffset int64  `json:"correct_offset"`
					} `json:"error"`
				}
				if json.Unmarshal(b, &conflictBody) == nil && conflictBody.Error.Tag == "incorrect_offset" {
					expectedNext := offset + chunkLen
					if conflictBody.Error.CorrectOffset == expectedNext {
						return b, nil
					}
				}
			}
			if resp.StatusCode == http.StatusUnauthorized {
				if !authRefreshed {
					if _, refErr := d.refreshTokenIfStale(tok); refErr != nil {
						return nil, refErr
					}
					authRefreshed = true
					didRefreshThisIter = true
					lastErr = newProviderHTTPError("dropbox "+endpoint, resp, b)
					continue
				}
				return nil, newProviderHTTPError("dropbox "+endpoint, resp, b)
			}
			lastErr = newProviderHTTPError("dropbox "+endpoint, resp, b)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return nil, lastErr
			}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
				if wErr := waitChunkRetryAfter(ctx, resp, 0); wErr != nil {
					return nil, wErr
				}
			}
			attempt++
		}
		return nil, lastErr
	}

	// Pour les petits fichiers (<= dropboxChunkSize) : upload direct
	if fileSize <= dropboxChunkSize {
		respBytes, err := uploadSegment("https://content.dropboxapi.com/2/files/upload", map[string]any{
			"path":       targetPath,
			"mode":       "overwrite",
			"autorename": false,
			"mute":       true,
		}, 0, fileSize)
		if err != nil {
			return "", fmt.Errorf("dropbox uploadZip : %w", err)
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

	// Pour les fichiers > dropboxChunkSize : session d'upload par blocs
	firstChunk := dropboxChunkSize
	startBytes, err := uploadSegment("https://content.dropboxapi.com/2/files/upload_session/start", map[string]any{"close": false}, 0, firstChunk)
	if err != nil {
		return "", err
	}

	var sRes struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(startBytes, &sRes); err != nil || sRes.SessionID == "" {
		return "", fmt.Errorf("dropbox session_id invalide : %v", err)
	}
	sessionID := sRes.SessionID

	offset := firstChunk
	for offset+dropboxChunkSize < fileSize {
		chunkLen := dropboxChunkSize
		if _, err := uploadSegment("https://content.dropboxapi.com/2/files/upload_session/append_v2", map[string]any{
			"cursor": map[string]any{
				"session_id": sessionID,
				"offset":     offset,
			},
			"close": false,
		}, offset, chunkLen); err != nil {
			return "", err
		}
		offset += chunkLen
	}

	// Dernier bloc : upload_session/finish
	finalLen := fileSize - offset
	finBytes, err := uploadSegment("https://content.dropboxapi.com/2/files/upload_session/finish", map[string]any{
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
	}, offset, finalLen)
	if err != nil {
		return "", err
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(finBytes, &res); err != nil {
		return "", err
	}

	InvalidateCloudIndexCache()
	if onProgress != nil {
		onProgress(100)
	}
	return res.ID, nil
}

func (d *DropboxProvider) UploadJSON(ctx context.Context, name string, data any, existingID string) (string, error) {
	if ctx == nil {
		ctx = baseContext()
	}
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	targetPath := "/" + name
	if existingID != "" && strings.HasPrefix(existingID, "id:") {
		targetPath = existingID
	}

	arg, err := marshalDropboxAPIArg(map[string]any{
		"path":       targetPath,
		"mode":       "overwrite",
		"autorename": false,
		"mute":       true,
	})
	if err != nil {
		return "", err
	}

	tok, err := d.getAccessToken()
	if err != nil {
		return "", err
	}

	authRefreshed := false
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			tok, err = d.getAccessToken()
			if err != nil {
				return "", err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://content.dropboxapi.com/2/files/upload", bytes.NewReader(jsonBytes))
		if err != nil {
			return "", err
		}
		req.ContentLength = int64(len(jsonBytes))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Dropbox-API-Arg", arg)

		resp, err := d.client.Do(req)
		if err != nil {
			return "", err
		}

		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && !authRefreshed {
			if _, refErr := d.refreshTokenIfStale(tok); refErr != nil {
				return "", refErr
			}
			authRefreshed = true
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return "", newProviderHTTPError("dropbox uploadJSON", resp, respBytes)
		}

		var res struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBytes, &res); err != nil {
			return "", fmt.Errorf("dropbox uploadJSON parsing : %w", err)
		}
		InvalidateCloudIndexCache()
		return res.ID, nil
	}
	return "", fmt.Errorf("dropbox uploadJSON : échec après rafraîchissement du token")
}

func isDropboxNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if idx := strings.IndexByte(msg, '{'); idx >= 0 {
		var payload struct {
			ErrorSummary string `json:"error_summary"`
			Error        struct {
				Tag        string `json:".tag"`
				PathLookup struct {
					Tag string `json:".tag"`
				} `json:"path_lookup"`
				Path struct {
					Tag string `json:".tag"`
				} `json:"path"`
				FromLookup struct {
					Tag string `json:".tag"`
				} `json:"from_lookup"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(msg[idx:]), &payload) == nil {
			if payload.Error.Tag == "not_found" ||
				payload.Error.PathLookup.Tag == "not_found" ||
				payload.Error.Path.Tag == "not_found" ||
				payload.Error.FromLookup.Tag == "not_found" {
				return true
			}
			if payload.ErrorSummary != "" {
				return strings.Contains(payload.ErrorSummary, "not_found")
			}
			return false
		}
	}
	return strings.Contains(msg, "path_lookup/not_found") || strings.Contains(msg, "path/not_found") || strings.Contains(msg, "from_lookup/not_found")
}

func (d *DropboxProvider) DeleteFile(ctx context.Context, fileID string) error {
	if ctx == nil {
		ctx = baseContext()
	}
	_, err := d.doRPC(ctx, "files/delete_v2", map[string]string{"path": fileID})
	if err == nil {
		InvalidateCloudIndexCache()
		return nil
	}
	if isDropboxNotFoundError(err) {
		InvalidateCloudIndexCache()
		return ErrNotFound
	}
	return err
}

// RenameFile déplace un fichier staging vers son nom définitif sur Dropbox de manière réversible
// (mise en backup temporaire de l'ancienne cible avant promotion, rollback si le déplacement échoue).
func (d *DropboxProvider) RenameFile(ctx context.Context, fileID string, newName string, existingTargetID string) (string, error) {
	if ctx == nil {
		ctx = baseContext()
	}
	targetPath := "/" + newName
	var backupTempPath string

	if existingTargetID != "" && existingTargetID != fileID {
		backupTempPath = fmt.Sprintf("/.__staging_bak_%d_%s", time.Now().UnixMilli(), newName)
		if _, bakErr := d.doRPC(ctx, "files/move_v2", map[string]any{
			"from_path":  existingTargetID,
			"to_path":    backupTempPath,
			"autorename": false,
		}); bakErr != nil {
			if !isDropboxNotFoundError(bakErr) {
				return "", fmt.Errorf("[dropbox] mise en sauvegarde ancienne cible avant promotion : %w", bakErr)
			}
			backupTempPath = ""
		}
	}

	respBytes, err := d.doRPC(ctx, "files/move_v2", map[string]any{
		"from_path":  fileID,
		"to_path":    targetPath,
		"autorename": false,
	})
	if err != nil {
		if backupTempPath != "" {
			if _, rbErr := d.doRPC(ctx, "files/move_v2", map[string]any{
				"from_path":  backupTempPath,
				"to_path":    targetPath,
				"autorename": false,
			}); rbErr != nil {
				ipc.Logf("[dropbox] avertissement : échec rollback backup %s vers %s : %v", backupTempPath, targetPath, rbErr)
			}
		}
		return "", err
	}

	if backupTempPath != "" {
		if delErr := d.DeleteFile(ctx, backupTempPath); delErr != nil && !errors.Is(delErr, ErrNotFound) {
			ipc.Logf("[dropbox] avertissement : suppression backup temporaire %s échouée : %v", backupTempPath, delErr)
		}
	}

	var res struct {
		Metadata struct {
			ID string `json:"id"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(respBytes, &res)
	promotedID := res.Metadata.ID
	if promotedID == "" {
		promotedID = fileID
	}
	InvalidateCloudIndexCache()
	return promotedID, nil
}

func (d *DropboxProvider) GetQuota(ctx context.Context) (int64, int64, error) {
	if ctx == nil {
		ctx = baseContext()
	}
	respBytes, err := d.doRPC(ctx, "users/get_space_usage", nil)
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

// GetFileModifiedTime récupère le timestamp serveur d'un fichier Dropbox par son ID ou chemin.
func (d *DropboxProvider) GetFileModifiedTime(ctx context.Context, fileID string) (time.Time, error) {
	if ctx == nil {
		ctx = baseContext()
	}
	if fileID == "" {
		return time.Time{}, fmt.Errorf("dropbox getFileModifiedTime: fileID vide")
	}
	respBytes, err := d.doRPC(ctx, "files/get_metadata", map[string]any{
		"path": fileID,
	})
	if err != nil {
		if isDropboxNotFoundError(err) {
			return time.Time{}, ErrNotFound
		}
		return time.Time{}, err
	}

	var res struct {
		ServerModified string `json:"server_modified"`
	}
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, res.ServerModified)
}
