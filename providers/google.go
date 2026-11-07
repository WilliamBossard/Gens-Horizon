package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
)

type GoogleProvider struct {
	tokenData auth.TokenData
	creds     config.ProviderConfig
	tokenPath string
	client    *http.Client
	mu        sync.Mutex
}

func NewGoogleProvider(tokenData auth.TokenData, creds config.ProviderConfig, tokenPath string) *GoogleProvider {
	return &GoogleProvider{
		tokenData: tokenData,
		creds:     creds,
		tokenPath: tokenPath,
		client:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (g *GoogleProvider) Name() string {
	return "google"
}

func (g *GoogleProvider) getAccessToken() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.tokenData.AccessToken == "" || g.tokenData.ExpiryDate == 0 || time.Now().UnixMilli() > (g.tokenData.ExpiryDate-60000) {
		if err := g.refreshToken(); err != nil {
			return "", err
		}
	}
	return g.tokenData.AccessToken, nil
}

func (g *GoogleProvider) refreshToken() error {
	if g.tokenData.RefreshToken == "" {
		return errors.New("aucun refresh_token disponible")
	}

	form := url.Values{
		"client_id":     {g.creds.ClientID},
		"client_secret": {g.creds.ClientSecret},
		"refresh_token": {g.tokenData.RefreshToken},
		"grant_type":    {"refresh_token"},
	}

	resp, err := http.PostForm("https://oauth2.googleapis.com/token", form)
	if err != nil {
		return fmt.Errorf("erreur requête refresh token : %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("échec rafraîchissement token (HTTP %d) : %s", resp.StatusCode, string(bodyBytes))
	}

	var res struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return err
	}

	g.tokenData.AccessToken = res.AccessToken
	g.tokenData.ExpiryDate = time.Now().UnixMilli() + (res.ExpiresIn * 1000)
	if res.RefreshToken != "" {
		g.tokenData.RefreshToken = res.RefreshToken
	}

	if err := auth.EncryptToken(g.tokenPath, g.tokenData); err != nil {
		ipc.Logf("[google] Échec sauvegarde token rafraîchi : %v", err)
	}

	return nil
}

func (g *GoogleProvider) doRequest(req *http.Request) (*http.Response, error) {
	tok, err := g.getAccessToken()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		g.mu.Lock()
		_ = g.refreshToken()
		g.mu.Unlock()

		newTok, err := g.getAccessToken()
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+newTok)
		return g.client.Do(req)
	}

	return resp, nil
}

func (g *GoogleProvider) ListFiles(nameContains string) ([]CloudFile, error) {
	var files []CloudFile
	var pageToken string
	maxPages := 20

	for page := 0; page < maxPages; page++ {
		params := url.Values{
			"spaces":   {"appDataFolder"},
			"fields":   {"nextPageToken, files(id, name, modifiedTime, size)"},
			"pageSize": {"1000"},
		}
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		queries := []string{"trashed = false"}
		if nameContains != "" {
			queries = append(queries, fmt.Sprintf("name contains '%s'", strings.ReplaceAll(nameContains, "'", "\\'")))
		}
		params.Set("q", strings.Join(queries, " and "))

		reqURL := "https://www.googleapis.com/drive/v3/files?" + params.Encode()
		req, err := http.NewRequest(http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := g.doRequest(req)
		if err != nil {
			return nil, err
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("[google] listFiles HTTP %d : %s", resp.StatusCode, string(bodyBytes))
		}

		var res struct {
			NextPageToken string `json:"nextPageToken"`
			Files         []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				ModifiedTime string `json:"modifiedTime"`
				Size         string `json:"size"`
			} `json:"files"`
		}
		if err := json.Unmarshal(bodyBytes, &res); err != nil {
			return nil, err
		}

		for _, f := range res.Files {
			modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)
			size, _ := strconv.ParseInt(f.Size, 10, 64)
			files = append(files, CloudFile{
				ID:           f.ID,
				Name:         f.Name,
				ModifiedTime: modTime,
				Size:         size,
			})
		}

		pageToken = res.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return files, nil
}

func (g *GoogleProvider) DownloadFile(fileID string, destPath string, onProgress func(pct int), totalSize int64) error {
	buildURL := func(confirm bool) string {
		u := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?alt=media", fileID)
		if confirm {
			u += "&acknowledgeAbuse=true"
		}
		return u
	}

	dlClient := &http.Client{Timeout: 10 * time.Minute}
	confirmAbuse := false

	return downloadWithResume(destPath, totalSize, onProgress, func(offset int64) (*http.Response, error) {
		tok, err := g.getAccessToken()
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequest(http.MethodGet, buildURL(confirmAbuse), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		resp, err := dlClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			g.mu.Lock()
			_ = g.refreshToken()
			g.mu.Unlock()
			newTok, _ := g.getAccessToken()
			req, _ = http.NewRequest(http.MethodGet, buildURL(confirmAbuse), nil)
			req.Header.Set("Authorization", "Bearer "+newTok)
			if offset > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			}
			resp, err = dlClient.Do(req)
			if err != nil {
				return nil, err
			}
		}

		contentType := resp.Header.Get("Content-Type")
		if resp.StatusCode == http.StatusOK && (strings.Contains(contentType, "text/html") || (strings.Contains(contentType, "application/json") && !strings.Contains(contentType, "octet"))) {
			_ = resp.Body.Close()
			confirmAbuse = true
			req, _ = http.NewRequest(http.MethodGet, buildURL(true), nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			if offset > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			}
			resp, err = dlClient.Do(req)
			if err != nil {
				return nil, err
			}
		}

		return resp, nil
	})
}

func (g *GoogleProvider) DownloadJSON(fileID string, target any) error {
	reqURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?alt=media", fileID)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := g.doRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("[google] downloadJSON HTTP %d : %s", resp.StatusCode, string(b))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

func (g *GoogleProvider) UploadZip(name string, srcPath string, existingID string, onProgress func(pct int)) (string, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	fileSize := info.Size()

	initMethod := "POST"
	initURL := "https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&fields=id,name,size,modifiedTime"
	if existingID != "" {
		initMethod = "PATCH"
		initURL = fmt.Sprintf("https://www.googleapis.com/upload/drive/v3/files/%s?uploadType=resumable&fields=id,name,size,modifiedTime", existingID)
	}

	metadata := map[string]any{"name": name}
	if existingID == "" {
		metadata["parents"] = []string{"appDataFolder"}
	}
	metaBytes, _ := json.Marshal(metadata)

	req, err := http.NewRequest(initMethod, initURL, bytes.NewReader(metaBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", "application/zip")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(fileSize, 10))

	resp, err := g.doRequest(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("[google] initiation upload resumable échouée : HTTP %d", resp.StatusCode)
	}

	uploadURL := resp.Header.Get("Location")
	if uploadURL == "" {
		return "", errors.New("[google] aucune URL d'upload retournée")
	}

	// Validation de sécurité de l'URL d'upload
	parsedU, err := url.Parse(uploadURL)
	if err != nil || (!strings.HasSuffix(parsedU.Hostname(), "googleapis.com") && parsedU.Hostname() != "googleapis.com") {
		return "", fmt.Errorf("[google] sécurité : URL d'upload suspecte %s", uploadURL)
	}

	// Upload par blocs (chunks de 8 Mo = multiple de 256 Ko requis par Google Drive)
	const googleChunkSize = int64(8 * 1024 * 1024)

	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	uploadClient := &http.Client{Timeout: 5 * time.Minute}

	if fileSize == 0 {
		putReq, err := http.NewRequest(http.MethodPut, uploadURL, http.NoBody)
		if err != nil {
			return "", err
		}
		putReq.ContentLength = 0
		putReq.Header.Set("Content-Range", "bytes */0")
		putResp, err := uploadClient.Do(putReq)
		if err != nil {
			return "", err
		}
		defer putResp.Body.Close()
		respBytes, _ := io.ReadAll(putResp.Body)
		if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusCreated {
			return "", fmt.Errorf("[google] échec upload ZIP vide (HTTP %d) : %s", putResp.StatusCode, string(respBytes))
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

	var offset int64
	var processed int64
	lastPct := -1

	for offset < fileSize {
		remaining := fileSize - offset
		chunkLen := googleChunkSize
		if remaining < chunkLen {
			chunkLen = remaining
		}

		var chunkDone bool
		var lastErr error

		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*1500) * time.Millisecond)
				if serverOffset, completedBody, qErr := queryGoogleUploadOffset(uploadClient, uploadURL, fileSize); qErr == nil {
					if completedBody != nil {
						var res struct {
							ID string `json:"id"`
						}
						if err := json.Unmarshal(completedBody, &res); err == nil && res.ID != "" {
							InvalidateCloudIndexCache()
							if onProgress != nil {
								onProgress(100)
							}
							return res.ID, nil
						}
					}
					if serverOffset >= 0 && serverOffset <= fileSize {
						offset = serverOffset
						remaining = fileSize - offset
						if remaining <= 0 {
							break
						}
						chunkLen = googleChunkSize
						if remaining < chunkLen {
							chunkLen = remaining
						}
					}
				}
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

			putReq, err := http.NewRequest(http.MethodPut, uploadURL, pr)
			if err != nil {
				return "", err
			}
			putReq.ContentLength = chunkLen
			putReq.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+chunkLen-1, fileSize))

			putResp, err := uploadClient.Do(putReq)
			if err != nil {
				lastErr = err
				continue
			}

			respBytes, _ := io.ReadAll(putResp.Body)
			_ = putResp.Body.Close()

			// 308 Resume Incomplete : chunk validé par Google Drive
			if putResp.StatusCode == http.StatusPermanentRedirect {
				if nextOff := parseGoogleRangeHeader(putResp.Header.Get("Range")); nextOff > offset {
					offset = nextOff
				} else {
					offset += chunkLen
				}
				chunkDone = true
				break
			}

			// 200 OK ou 201 Created : upload complet terminé
			if putResp.StatusCode == http.StatusOK || putResp.StatusCode == http.StatusCreated {
				var res struct {
					ID   string `json:"id"`
					Size string `json:"size"`
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

			lastErr = fmt.Errorf("[google] échec upload chunk (HTTP %d) : %s", putResp.StatusCode, string(respBytes))
			if putResp.StatusCode >= 400 && putResp.StatusCode < 500 && putResp.StatusCode != http.StatusTooManyRequests {
				return "", lastErr
			}
		}

		if !chunkDone {
			if lastErr != nil {
				return "", lastErr
			}
			return "", errors.New("[google] échec inattendu lors de l'upload par blocs")
		}
	}

	return "", errors.New("[google] upload terminé sans réponse finale du serveur")
}

func parseGoogleRangeHeader(rangeHdr string) int64 {
	// Format attendu : "bytes=0-8388607"
	if !strings.HasPrefix(rangeHdr, "bytes=") {
		return -1
	}
	parts := strings.Split(strings.TrimPrefix(rangeHdr, "bytes="), "-")
	if len(parts) != 2 {
		return -1
	}
	endByte, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || endByte < 0 {
		return -1
	}
	return endByte + 1
}

func queryGoogleUploadOffset(client *http.Client, uploadURL string, totalSize int64) (int64, []byte, error) {
	req, err := http.NewRequest(http.MethodPut, uploadURL, http.NoBody)
	if err != nil {
		return -1, nil, err
	}
	req.ContentLength = 0
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))

	resp, err := client.Do(req)
	if err != nil {
		return -1, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return totalSize, body, nil
	}
	if resp.StatusCode == http.StatusPermanentRedirect {
		nextOff := parseGoogleRangeHeader(resp.Header.Get("Range"))
		if nextOff < 0 {
			return 0, nil, nil
		}
		return nextOff, nil, nil
	}
	return -1, nil, fmt.Errorf("status %d", resp.StatusCode)
}

func (g *GoogleProvider) UploadJSON(name string, data any, existingID string) (string, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	method := "POST"
	reqURL := "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	if existingID != "" {
		method = "PATCH"
		reqURL = fmt.Sprintf("https://www.googleapis.com/upload/drive/v3/files/%s?uploadType=multipart", existingID)
	}

	metadata := map[string]any{"name": name}
	if existingID == "" {
		metadata["parents"] = []string{"appDataFolder"}
	}
	metaBytes, _ := json.Marshal(metadata)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Part 1 : Metadata
	h := make(textproto.MIMEHeader)
	h.Set("Content-Type", "application/json; charset=UTF-8")
	part1, err := writer.CreatePart(h)
	if err != nil {
		return "", err
	}
	_, _ = part1.Write(metaBytes)

	// Part 2 : JSON Content
	h2 := make(textproto.MIMEHeader)
	h2.Set("Content-Type", "application/json")
	part2, err := writer.CreatePart(h2)
	if err != nil {
		return "", err
	}
	_, _ = part2.Write(jsonBytes)
	_ = writer.Close()

	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", fmt.Sprintf("multipart/related; boundary=%s", writer.Boundary()))

	resp, err := g.doRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("[google] uploadJSON HTTP %d : %s", resp.StatusCode, string(respBytes))
	}

	var res struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(respBytes, &res)
	InvalidateCloudIndexCache()
	return res.ID, nil
}

func (g *GoogleProvider) DeleteFile(fileID string) error {
	reqURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s", fileID)
	req, err := http.NewRequest(http.MethodDelete, reqURL, nil)
	if err != nil {
		return err
	}
	resp, err := g.doRequest(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("[google] deleteFile HTTP %d", resp.StatusCode)
	}
	InvalidateCloudIndexCache()
	return nil
}

func (g *GoogleProvider) GetQuota() (int64, int64, error) {
	reqURL := "https://www.googleapis.com/drive/v3/about?fields=storageQuota"
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := g.doRequest(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("[google] getQuota HTTP %d : %s", resp.StatusCode, string(bodyBytes))
	}

	var res struct {
		StorageQuota struct {
			Usage string `json:"usage"`
			Limit string `json:"limit"`
		} `json:"storageQuota"`
	}
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return 0, 0, err
	}

	used, _ := strconv.ParseInt(res.StorageQuota.Usage, 10, 64)
	total, _ := strconv.ParseInt(res.StorageQuota.Limit, 10, 64)
	return used, total, nil
}

