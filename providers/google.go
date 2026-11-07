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
		return fmt.Errorf("erreur requÃªte refresh token : %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ã©chec rafraÃ®chissement token (HTTP %d) : %s", resp.StatusCode, string(bodyBytes))
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
		ipc.Logf("[google] Ã‰chec sauvegarde token rafraÃ®chi : %v", err)
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
			"orderBy":  {"modifiedTime desc"},
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
	tok, err := g.getAccessToken()
	if err != nil {
		return err
	}

	buildURL := func(confirm bool) string {
		u := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?alt=media", fileID)
		if confirm {
			u += "&acknowledgeAbuse=true"
		}
		return u
	}

	req, err := http.NewRequest(http.MethodGet, buildURL(false), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	dlClient := &http.Client{Timeout: 10 * time.Minute}
	resp, err := dlClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		g.mu.Lock()
		_ = g.refreshToken()
		g.mu.Unlock()
		newTok, _ := g.getAccessToken()
		req, _ = http.NewRequest(http.MethodGet, buildURL(false), nil)
		req.Header.Set("Authorization", "Bearer "+newTok)
		resp, err = dlClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
	}

	// DÃ©tection page de confirmation Google Drive pour gros fichiers
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode == http.StatusOK && (strings.Contains(contentType, "text/html") || (strings.Contains(contentType, "application/json") && !strings.Contains(contentType, "octet"))) {
		_ = resp.Body.Close()
		req, _ = http.NewRequest(http.MethodGet, buildURL(true), nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err = dlClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("[google] Erreur tÃ©lÃ©chargement HTTP %d : %s", resp.StatusCode, string(b))
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 64*1024)
	var downloaded int64
	lastPct := -1

	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				_ = os.Remove(destPath)
				return wErr
			}
			downloaded += int64(n)
			if onProgress != nil && totalSize > 0 {
				pct := int((downloaded * 100) / totalSize)
				if pct > 100 {
					pct = 100
				}
				if pct != lastPct && (pct >= lastPct+2 || pct == 100) {
					onProgress(pct)
					lastPct = pct
				}
			}
		}
		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			_ = os.Remove(destPath)
			return rErr
		}
	}

	if totalSize > 0 && downloaded < totalSize {
		_ = os.Remove(destPath)
		return fmt.Errorf("tÃ©lÃ©chargement incomplet : %d / %d octets reÃ§us", downloaded, totalSize)
	}

	return nil
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
		return "", fmt.Errorf("[google] initiation upload resumable Ã©chouÃ©e : HTTP %d", resp.StatusCode)
	}

	uploadURL := resp.Header.Get("Location")
	if uploadURL == "" {
		return "", errors.New("[google] aucune URL d'upload retournÃ©e")
	}

	// Validation de sÃ©curitÃ© de l'URL d'upload
	parsedU, err := url.Parse(uploadURL)
	if err != nil || (!strings.HasSuffix(parsedU.Hostname(), "googleapis.com") && parsedU.Hostname() != "googleapis.com") {
		return "", fmt.Errorf("[google] sÃ©curitÃ© : URL d'upload suspecte %s", uploadURL)
	}

	// Streaming PUT vers l'uploadURL
	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	putReq, err := http.NewRequest(http.MethodPut, uploadURL, file)
	if err != nil {
		return "", err
	}
	putReq.ContentLength = fileSize
	if fileSize > 0 {
		putReq.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", fileSize-1, fileSize))
	} else {
		putReq.Header.Set("Content-Range", "bytes */0")
	}

	uploadClient := &http.Client{Timeout: 10 * time.Minute}
	putResp, err := uploadClient.Do(putReq)
	if err != nil {
		return "", err
	}
	defer putResp.Body.Close()

	respBytes, _ := io.ReadAll(putResp.Body)
	if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("[google] Ã©chec upload ZIP (HTTP %d) : %s", putResp.StatusCode, string(respBytes))
	}

	var res struct {
		ID   string `json:"id"`
		Size string `json:"size"`
	}
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return "", err
	}

	if onProgress != nil {
		onProgress(100)
	}

	return res.ID, nil
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

