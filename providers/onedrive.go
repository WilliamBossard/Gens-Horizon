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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/retry"
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

	if o.tokenData.AccessToken == "" || ((o.tokenData.ExpiryDate == 0 || time.Now().UnixMilli() > (o.tokenData.ExpiryDate-60000)) && o.tokenData.RefreshToken != "") {
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

	req, err := http.NewRequestWithContext(baseContext(), "POST", "https://login.microsoftonline.com/common/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("erreur refresh token OneDrive (HTTP %d) : %s", resp.StatusCode, string(bodyBytes)),
		}
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

func isAllowedOneDriveURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	allowedSuffixes := []string{
		"graph.microsoft.com",
		"onedrive.com",
		"1drv.com",
		"live.com",
		"sharepoint.com",
		"microsoft.com",
	}
	for _, s := range allowedSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

func (o *OneDriveProvider) refreshTokenIfStale(failedToken string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.tokenData.AccessToken != "" && o.tokenData.AccessToken != failedToken {
		return o.tokenData.AccessToken, nil
	}
	if o.tokenData.RefreshToken == "" {
		return "", errors.New("onedrive: aucun refresh_token disponible")
	}
	if err := o.refreshToken(); err != nil {
		return "", err
	}
	return o.tokenData.AccessToken, nil
}

func (o *OneDriveProvider) doRequest(req *http.Request) (*http.Response, error) {
	return o.doRequestWithClient(req, o.client)
}

func (o *OneDriveProvider) doRequestWithClient(req *http.Request, client *http.Client) (*http.Response, error) {
	tok, err := o.getAccessToken()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		newTok, rErr := o.refreshTokenIfStale(tok)
		if rErr != nil {
			return nil, rErr
		}
		if req.GetBody != nil {
			newBody, bErr := req.GetBody()
			if bErr != nil {
				return nil, bErr
			}
			req.Body = newBody
		}
		req.Header.Set("Authorization", "Bearer "+newTok)
		return client.Do(req)
	}

	return resp, nil
}

func (o *OneDriveProvider) ListFiles(nameContains string) ([]CloudFile, error) {
	var files []CloudFile
	nextURL := "https://graph.microsoft.com/v1.0/me/drive/special/approot/children?$top=1000"
	const maxPages = 100

	for page := 0; nextURL != "" && page < maxPages; page++ {
		req, err := http.NewRequest(http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := o.doRequest(req)
		if err != nil {
			return nil, err
		}

		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, &retry.HTTPError{
				StatusCode: resp.StatusCode,
				RetryAfter: retry.ParseRetryAfter(resp.Header.Get("Retry-After")),
				Message:    fmt.Sprintf("onedrive listFiles HTTP %d : %s", resp.StatusCode, string(bodyBytes)),
			}
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

		if res.NextLink != "" && !isAllowedOneDriveURL(res.NextLink) {
			return nil, fmt.Errorf("onedrive pagination URL suspecte : %s", res.NextLink)
		}
		nextURL = res.NextLink
	}

	if nextURL != "" {
		return nil, fmt.Errorf("%w : onedrive listFiles exceeded max pages", ErrListingTruncated)
	}

	return files, nil
}

func (o *OneDriveProvider) DownloadFile(fileID string, destPath string, onProgress func(pct int), totalSize int64) error {
	dlClient := streamingClient(o.client)
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/content", url.PathEscape(fileID))

	buildDLReq := func(token string, start, end int64) (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if end >= start {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		} else if start > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
		}
		return req, nil
	}

	return downloadSmart(destPath, totalSize, onProgress, func(start, end int64) (*http.Response, error) {
		tok, err := o.getAccessToken()
		if err != nil {
			return nil, err
		}

		req, err := buildDLReq(tok, start, end)
		if err != nil {
			return nil, err
		}

		resp, err := dlClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			newTok, rErr := o.refreshTokenIfStale(tok)
			if rErr != nil {
				return nil, rErr
			}
			req, err = buildDLReq(newTok, start, end)
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

func (o *OneDriveProvider) DownloadJSON(fileID string, target any) error {
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/content", url.PathEscape(fileID))
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("onedrive downloadJSON HTTP %d : %s", resp.StatusCode, string(b)),
		}
	}

	return json.NewDecoder(io.LimitReader(resp.Body, 16*1024*1024)).Decode(target)
}

func (o *OneDriveProvider) UploadZip(name string, srcPath string, existingID string, onProgress func(pct int)) (string, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	fileSize := info.Size()

	var processed int64
	lastPct := -1

	// Pour < 4 Mo : upload direct PUT simple avec ProgressReader (rembobinable sur retry 401 via GetBody)
	if fileSize < 4*1024*1024 {
		file, err := os.Open(srcPath)
		if err != nil {
			return "", err
		}
		defer file.Close()

		makeBody := func() (io.ReadCloser, error) {
			if _, sErr := file.Seek(0, io.SeekStart); sErr != nil {
				return nil, sErr
			}
			processed = 0
			lastPct = -1
			return io.NopCloser(&ProgressReader{
				Reader:     file,
				TotalSize:  fileSize,
				Processed:  &processed,
				LastPct:    &lastPct,
				OnProgress: onProgress,
			}), nil
		}

		initialBody, err := makeBody()
		if err != nil {
			return "", err
		}

		reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/special/approot:/%s:/content", url.PathEscape(name))
		req, err := http.NewRequest(http.MethodPut, reqURL, initialBody)
		if err != nil {
			return "", err
		}
		req.GetBody = makeBody
		req.ContentLength = fileSize
		req.Header.Set("Content-Type", "application/zip")

		uploadClient := clientWithTimeout(o.client, 5*time.Minute)
		resp, err := o.doRequestWithClient(req, uploadClient)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return "", &retry.HTTPError{
				StatusCode: resp.StatusCode,
				Message:    fmt.Sprintf("onedrive simple upload HTTP %d : %s", resp.StatusCode, string(b)),
			}
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

	sessBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return "", &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("onedrive createUploadSession HTTP %d : %s", resp.StatusCode, string(sessBytes)),
		}
	}

	var sessionRes struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(sessBytes, &sessionRes); err != nil || sessionRes.UploadURL == "" {
		return "", errors.New("aucune URL d'upload session retournée par OneDrive")
	}
	if !isAllowedOneDriveURL(sessionRes.UploadURL) {
		return "", fmt.Errorf("sécurité : URL d'upload session OneDrive suspecte %s", sessionRes.UploadURL)
	}

	// Upload par chunks de 10 Mo avec ProgressReader et reprise
	chunkSize := int64(10 * 1024 * 1024)
	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	uploadClient := clientWithTimeout(o.client, 5*time.Minute)
	var offset int64
	var finalID string

	querySessionNextOffset := func() (int64, bool) {
		req, qErr := http.NewRequest(http.MethodGet, sessionRes.UploadURL, nil)
		if qErr != nil {
			return 0, false
		}
		qResp, qErr := o.client.Do(req)
		if qErr != nil {
			return 0, false
		}
		defer qResp.Body.Close()
		if qResp.StatusCode != http.StatusOK {
			return 0, false
		}
		var status struct {
			NextExpectedRanges []string `json:"nextExpectedRanges"`
		}
		if err := json.NewDecoder(io.LimitReader(qResp.Body, 64*1024)).Decode(&status); err != nil || len(status.NextExpectedRanges) == 0 {
			return 0, false
		}
		rangeParts := strings.SplitN(status.NextExpectedRanges[0], "-", 2)
		nextStart, pErr := strconv.ParseInt(strings.TrimSpace(rangeParts[0]), 10, 64)
		if pErr != nil || nextStart < 0 {
			return 0, false
		}
		return nextStart, true
	}

	for offset < fileSize {
		remaining := fileSize - offset
		currentChunk := chunkSize
		if remaining < currentChunk {
			currentChunk = remaining
		}

		var chunkDone bool
		var lastErr error
		var nextOffset = offset + currentChunk

		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-baseContext().Done():
					return "", baseContext().Err()
				case <-time.After(time.Duration(attempt*1500) * time.Millisecond):
				}
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

			chunkResp, cleanup, err := doUploadWithIdleTimeout(uploadClient, http.MethodPut, sessionRes.UploadURL, pr, currentChunk, func(r *http.Request) {
				r.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+currentChunk-1, fileSize))
			})
			if err != nil {
				lastErr = err
				if expectedStart, ok := querySessionNextOffset(); ok && expectedStart > offset {
					nextOffset = expectedStart
					chunkDone = true
					break
				}
				continue
			}
			chunkBody, _ := io.ReadAll(io.LimitReader(chunkResp.Body, 1<<20))
			_ = chunkResp.Body.Close()
			cleanup()

			if chunkResp.StatusCode != http.StatusOK && chunkResp.StatusCode != http.StatusCreated && chunkResp.StatusCode != http.StatusAccepted {
				lastErr = fmt.Errorf("onedrive chunk upload HTTP %d : %s", chunkResp.StatusCode, string(chunkBody))
				if chunkResp.StatusCode == http.StatusRequestedRangeNotSatisfiable || chunkResp.StatusCode >= 500 || chunkResp.StatusCode == http.StatusTooManyRequests || chunkResp.StatusCode == http.StatusRequestTimeout {
					if expectedStart, ok := querySessionNextOffset(); ok && expectedStart > offset {
						nextOffset = expectedStart
						chunkDone = true
						break
					}
					continue
				}
				// Erreur 4xx définitive (400/401/403/404/413...) : inutile de réessayer
				if chunkResp.StatusCode >= 400 && chunkResp.StatusCode < 500 {
					break
				}
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
		offset = nextOffset
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

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("onedrive uploadJSON HTTP %d : %s", resp.StatusCode, string(b)),
		}
	}

	var res struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &res)
	InvalidateCloudIndexCache()
	return res.ID, nil
}

func (o *OneDriveProvider) DeleteFile(fileID string) error {
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s", url.PathEscape(fileID))
	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := o.doRequest(req)
	if err != nil {
		return err
	}
	errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("onedrive delete HTTP %d : %s", resp.StatusCode, string(errBytes)),
		}
	}
	InvalidateCloudIndexCache()
	return nil
}

// RenameFile renomme un fichier staging sur OneDrive (avec remplacement de cible si besoin).
func (o *OneDriveProvider) RenameFile(fileID string, newName string, existingTargetID string) (string, error) {
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s", url.PathEscape(fileID))
	patchPayload, _ := json.Marshal(map[string]any{
		"name":                              newName,
		"@microsoft.graph.conflictBehavior": "replace",
	})

	tryPatch := func() (*http.Response, []byte, error) {
		req, err := http.NewRequest(http.MethodPatch, reqURL, bytes.NewReader(patchPayload))
		if err != nil {
			return nil, nil, err
		}
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(patchPayload)), nil
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := o.doRequest(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		return resp, b, nil
	}

	resp, b, err := tryPatch()
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusConflict && existingTargetID != "" && existingTargetID != fileID {
		backupName := fmt.Sprintf("%s.bak.%d", newName, time.Now().UnixNano())
		backupPayload, _ := json.Marshal(map[string]string{"name": backupName})
		backupReq, bErr := http.NewRequestWithContext(baseContext(), "PATCH", fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s", url.PathEscape(existingTargetID)), bytes.NewReader(backupPayload))
		if bErr != nil {
			return "", fmt.Errorf("onedrive backup cible avant promotion : %w", bErr)
		}
		backupReq.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(backupPayload)), nil
		}
		backupReq.Header.Set("Content-Type", "application/json")
		backupResp, bErr := o.doRequest(backupReq)
		if bErr != nil {
			return "", fmt.Errorf("onedrive backup cible avant promotion : %w", bErr)
		}
		_, _ = io.ReadAll(io.LimitReader(backupResp.Body, 1024*1024))
		_ = backupResp.Body.Close()
		if backupResp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("onedrive backup cible HTTP %d", backupResp.StatusCode)
		}

		resp, b, err = tryPatch()
		if err != nil || resp.StatusCode != http.StatusOK {
			restoreReq, rErr := http.NewRequestWithContext(baseContext(), "PATCH", fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s", url.PathEscape(existingTargetID)), bytes.NewReader(patchPayload))
			if rErr == nil {
				restoreReq.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(patchPayload)), nil
				}
				restoreReq.Header.Set("Content-Type", "application/json")
				if rResp, rErr2 := o.doRequest(restoreReq); rErr2 == nil {
					_, _ = io.ReadAll(io.LimitReader(rResp.Body, 1024*1024))
					_ = rResp.Body.Close()
				}
			}
			if err != nil {
				return "", err
			}
		} else {
			_ = o.DeleteFile(existingTargetID)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("onedrive renameFile HTTP %d : %s", resp.StatusCode, string(b)),
		}
	}

	var res struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &res)
	promotedID := res.ID
	if promotedID == "" {
		promotedID = fileID
	}
	InvalidateCloudIndexCache()
	return promotedID, nil
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

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return 0, 0, &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("onedrive quota HTTP %d : %s", resp.StatusCode, string(b)),
		}
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

// GetFileModifiedTime récupère le timestamp serveur d'un élément OneDrive par son ID.
func (o *OneDriveProvider) GetFileModifiedTime(fileID string) (time.Time, error) {
	if fileID == "" {
		return time.Time{}, fmt.Errorf("onedrive getFileModifiedTime: fileID vide")
	}
	reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s?$select=lastModifiedDateTime", url.PathEscape(fileID))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := o.doRequest(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode == http.StatusNotFound {
		return time.Time{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, &retry.HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("onedrive getFileModifiedTime HTTP %d : %s", resp.StatusCode, string(b)),
		}
	}

	var res struct {
		LastModifiedDateTime string `json:"lastModifiedDateTime"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, res.LastModifiedDateTime)
}

