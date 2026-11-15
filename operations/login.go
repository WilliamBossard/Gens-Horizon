package operations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/lock"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
)

func createPKCEPair() (verifier string, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func createOAuthState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func openBrowser(targetURL string) error {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("URL OAuth refusée (HTTPS requis) : %s", targetURL)
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL).Start()
	case "darwin":
		return exec.Command("open", targetURL).Start()
	default:
		return exec.Command("xdg-open", targetURL).Start()
	}
}

func Login(args []string) {
	settings := paths.LoadSettings()
	providerName := paths.GetProviderName(args, settings)

	cfg, err := config.GetProviderConfig(providerName, paths.GetHorizonDataDir())
	if err != nil {
		ipc.SendError("CONFIG_ERROR", err.Error(), "")
		return
	}

	codeVerifier, codeChallenge := createPKCEPair()
	state := createOAuthState()
	u, err := url.Parse(cfg.RedirectURI)
	if err != nil {
		ipc.SendError("INVALID_REDIRECT_URI", err.Error(), "")
		return
	}

	port := u.Port()
	if port == "" {
		port = "12543"
	}

	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		ipc.SendError("PORT_IN_USE", fmt.Sprintf("Le port local %s est déjà utilisé : %v", port, err), "")
		return
	}
	defer listener.Close()

	authURL := ""
	switch providerName {
	case "google":
		params := url.Values{
			"client_id":              {cfg.ClientID},
			"redirect_uri":           {cfg.RedirectURI},
			"response_type":          {"code"},
			"scope":                  {"https://www.googleapis.com/auth/drive.appdata"},
			"access_type":            {"offline"},
			"prompt":                 {"consent"},
			"state":                  {state},
			"include_granted_scopes": {"true"},
			"code_challenge":         {codeChallenge},
			"code_challenge_method":  {"S256"},
		}
		authURL = cfg.AuthURI + "?" + params.Encode()
	case "dropbox":
		params := url.Values{
			"client_id":             {cfg.ClientID},
			"redirect_uri":          {cfg.RedirectURI},
			"response_type":         {"code"},
			"token_access_type":     {"offline"},
			"state":                 {state},
			"code_challenge":        {codeChallenge},
			"code_challenge_method": {"S256"},
		}
		authURL = cfg.AuthURI + "?" + params.Encode()
	case "onedrive":
		scope := cfg.Scope
		if scope == "" {
			scope = "Files.ReadWrite offline_access User.Read"
		}
		params := url.Values{
			"client_id":             {cfg.ClientID},
			"redirect_uri":          {cfg.RedirectURI},
			"response_type":         {"code"},
			"scope":                 {scope},
			"state":                 {state},
			"code_challenge":        {codeChallenge},
			"code_challenge_method": {"S256"},
		}
		authURL = cfg.AuthURI + "?" + params.Encode()
	}

	parsedAuth, err := url.Parse(cfg.AuthURI)
	if err != nil || parsedAuth.Scheme != "https" || parsedAuth.Host == "" {
		ipc.SendError("INVALID_AUTH_URI", "AuthURI OAuth doit utiliser HTTPS.", "")
		return
	}
	parsedToken, err := url.Parse(cfg.TokenURI)
	if err != nil || parsedToken.Scheme != "https" || parsedToken.Host == "" {
		ipc.SendError("INVALID_TOKEN_URI", "TokenURI OAuth doit utiliser HTTPS.", "")
		return
	}

	ipc.Logf("[login] Ouverture du navigateur vers : %s", authURL)
	if bErr := openBrowser(authURL); bErr != nil {
		_ = listener.Close()
		ipc.SendError("BROWSER_ERROR", bErr.Error(), "")
		return
	}

	mux := http.NewServeMux()
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan struct{})
	var closeDoneOnce sync.Once
	signalDone := func() {
		closeDoneOnce.Do(func() {
			close(done)
		})
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		q := r.URL.Query()
		if q.Get("error") != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#1e1e1e;color:#fff;text-align:center;padding:50px;'><h2>Connexion annulée</h2><p>%s</p></body></html>", html.EscapeString(q.Get("error")))
			ipc.SendError("AUTH_CANCELLED", "La demande de connexion a été refusée ou annulée.", "")
			signalDone()
			return
		}

		retState := q.Get("state")
		if retState != state {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#1e1e1e;color:#fff;text-align:center;padding:50px;'><h2>Erreur CSRF</h2><p>Le jeton de session est invalide.</p></body></html>")
			ipc.Logf("[login] Requête OAuth ignorée (state invalide)")
			return
		}

		code := q.Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body>Code OAuth manquant</body></html>")
			signalDone()
			return
		}

		// Échange du code d'autorisation contre des jetons
		tokenData, err := exchangeCodeForToken(providerName, cfg, code, codeVerifier)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#1e1e1e;color:#fff;text-align:center;padding:50px;'><h2>Échec de la connexion</h2><p>%s</p></body></html>", html.EscapeString(err.Error()))
			ipc.SendError("EXCHANGE_ERROR", err.Error(), "")
			signalDone()
			return
		}

		lockAcquired := false
		for i := 0; i < 10; i++ {
			if lock.AcquireLock() {
				lockAcquired = true
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
		if lockAcquired {
			defer lock.ReleaseLock()
		} else {
			ipc.Logf("[login] AVERTISSEMENT : horizon.lock occupé lors de la sauvegarde du token, écriture atomique directe.")
		}

		tokenPath := providers.GetTokenPath(providerName)
		if err := auth.EncryptToken(tokenPath, tokenData); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "<html><body>Erreur sauvegarde token : %s</body></html>", html.EscapeString(err.Error()))
			ipc.SendError("TOKEN_SAVE_ERROR", err.Error(), "")
			signalDone()
			return
		}
		providers.InvalidateCloudIndexCache()

		// Sauvegarde du provider sélectionné dans horizon_settings.json
		settings.Provider = providerName
		settingsPath := paths.GetSettingsPath()
		if b, err := json.MarshalIndent(settings, "", "  "); err == nil {
			_ = osWriteFileAtomic(settingsPath, b, 0644)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Connexion Réussie</title><style>body{background:#18181b;color:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.card{background:#27272a;padding:40px;border-radius:12px;box-shadow:0 10px 25px rgba(0,0,0,0.5);text-align:center;max-width:400px}h2{color:#22c55e;margin-top:0}p{color:#a1a1aa;line-height:1.5}</style></head><body><div class="card"><h2>Connexion réussie !</h2><p>Votre compte Cloud a été associé avec succès à Gens Launcher.<br><br>Vous pouvez fermer cet onglet et continuer à jouer.</p></div></body></html>`)

		ipc.Send(ipc.BaseMessage{
			Type:      "SUCCESS",
			Message:   "Authentication successful! Token saved.",
			ErrorCode: "AUTH_SUCCESS",
		})
		signalDone()
	})

	go func() {
		_ = server.Serve(listener)
	}()

	select {
	case <-done:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	case <-time.After(5 * time.Minute):
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		ipc.SendError("TIMEOUT", "Délai d'attente d'authentification dépassé (5 minutes).", "")
	}
}

func exchangeCodeForToken(providerName string, cfg config.ProviderConfig, code string, codeVerifier string) (auth.TokenData, error) {
	var tokenData auth.TokenData
	httpClient := &http.Client{Timeout: 30 * time.Second}

	switch providerName {
	case "google":
		form := url.Values{
			"code":          {code},
			"client_id":     {cfg.ClientID},
			"client_secret": {cfg.ClientSecret},
			"redirect_uri":  {cfg.RedirectURI},
			"grant_type":    {"authorization_code"},
		}
		if codeVerifier != "" {
			form.Set("code_verifier", codeVerifier)
		}
		resp, err := httpClient.PostForm(cfg.TokenURI, form)
		if err != nil {
			return tokenData, err
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return tokenData, fmt.Errorf("google token exchange HTTP %d : %s", resp.StatusCode, string(b))
		}

		var res struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			TokenType    string `json:"token_type"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return tokenData, err
		}
		tokenData.AccessToken = res.AccessToken
		tokenData.RefreshToken = res.RefreshToken
		tokenData.TokenType = res.TokenType
		tokenData.ExpiryDate = time.Now().UnixMilli() + (res.ExpiresIn * 1000)

	case "dropbox":
		form := url.Values{
			"code":          {code},
			"client_id":     {cfg.ClientID},
			"client_secret": {cfg.ClientSecret},
			"redirect_uri":  {cfg.RedirectURI},
			"grant_type":    {"authorization_code"},
		}
		if codeVerifier != "" {
			form.Set("code_verifier", codeVerifier)
		}
		resp, err := httpClient.PostForm(cfg.TokenURI, form)
		if err != nil {
			return tokenData, err
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return tokenData, fmt.Errorf("dropbox token exchange HTTP %d : %s", resp.StatusCode, string(b))
		}

		var res struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return tokenData, err
		}
		tokenData.AccessToken = res.AccessToken
		tokenData.RefreshToken = res.RefreshToken
		if res.ExpiresIn > 0 {
			tokenData.ExpiryDate = time.Now().UnixMilli() + (res.ExpiresIn * 1000)
		}

	case "onedrive":
		form := url.Values{
			"code":         {code},
			"client_id":    {cfg.ClientID},
			"redirect_uri": {cfg.RedirectURI},
			"grant_type":   {"authorization_code"},
		}
		if codeVerifier != "" {
			form.Set("code_verifier", codeVerifier)
		}
		if cfg.ClientSecret != "" {
			form.Set("client_secret", cfg.ClientSecret)
		}
		resp, err := httpClient.PostForm(cfg.TokenURI, form)
		if err != nil {
			return tokenData, err
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return tokenData, fmt.Errorf("onedrive token exchange HTTP %d : %s", resp.StatusCode, string(b))
		}

		var res struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			TokenType    string `json:"token_type"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return tokenData, err
		}
		tokenData.AccessToken = res.AccessToken
		tokenData.RefreshToken = res.RefreshToken
		tokenData.TokenType = res.TokenType
		if res.ExpiresIn > 0 {
			tokenData.ExpiryDate = time.Now().UnixMilli() + (res.ExpiresIn * 1000)
		}
	}

	return tokenData, nil
}

func osWriteFileAtomic(filePath string, data []byte, perm uint32) error {
	var rBytes [4]byte
	_, _ = rand.Read(rBytes[:])
	tmp := fmt.Sprintf("%s.tmp.%d.%s", filePath, os.Getpid(), hex.EncodeToString(rBytes[:]))
	if err := os.WriteFile(tmp, data, os.FileMode(perm)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
