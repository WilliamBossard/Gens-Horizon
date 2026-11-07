package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/ipc"
	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
)

func createOAuthState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func openBrowser(targetURL string) error {
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
		ipc.SendError("PORT_IN_USE", fmt.Sprintf("Le port local %s est dÃ©jÃ  utilisÃ© : %v", port, err), "")
		return
	}

	authURL := ""
	switch providerName {
	case "google":
		params := url.Values{
			"client_id":             {cfg.ClientID},
			"redirect_uri":          {cfg.RedirectURI},
			"response_type":         {"code"},
			"scope":                 {"https://www.googleapis.com/auth/drive.appdata"},
			"access_type":           {"offline"},
			"prompt":                {"consent"},
			"state":                 {state},
			"include_granted_scopes": {"true"},
		}
		authURL = cfg.AuthURI + "?" + params.Encode()
	case "dropbox":
		params := url.Values{
			"client_id":          {cfg.ClientID},
			"redirect_uri":       {cfg.RedirectURI},
			"response_type":      {"code"},
			"token_access_type":  {"offline"},
			"state":              {state},
		}
		authURL = cfg.AuthURI + "?" + params.Encode()
	case "onedrive":
		scope := cfg.Scope
		if scope == "" {
			scope = "Files.ReadWrite offline_access User.Read"
		}
		params := url.Values{
			"client_id":     {cfg.ClientID},
			"redirect_uri":  {cfg.RedirectURI},
			"response_type": {"code"},
			"scope":         {scope},
			"state":         {state},
		}
		authURL = cfg.AuthURI + "?" + params.Encode()
	}

	ipc.Logf("[login] Ouverture du navigateur vers : %s", authURL)
	_ = openBrowser(authURL)

	server := &http.Server{}
	done := make(chan struct{})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("error") != "" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#1e1e1e;color:#fff;text-align:center;padding:50px;'><h2>Connexion annulÃ©e</h2><p>%s</p></body></html>", q.Get("error"))
			ipc.SendError("AUTH_CANCELLED", "La demande de connexion a Ã©tÃ© refusÃ©e ou annulÃ©e.", "")
			close(done)
			return
		}

		retState := q.Get("state")
		if retState != state {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#1e1e1e;color:#fff;text-align:center;padding:50px;'><h2>Erreur CSRF</h2><p>Le jeton de session est invalide.</p></body></html>")
			ipc.SendError("CSRF_ERROR", "Jeton de sÃ©curitÃ© state invalide.", "")
			close(done)
			return
		}

		code := q.Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body>Code OAuth manquant</body></html>")
			close(done)
			return
		}

		// Ã‰change du code d'autorisation contre des jetons
		tokenData, err := exchangeCodeForToken(providerName, cfg, code)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "<html><body style='font-family:sans-serif;background:#1e1e1e;color:#fff;text-align:center;padding:50px;'><h2>Ã‰chec de la connexion</h2><p>%v</p></body></html>", err)
			ipc.SendError("EXCHANGE_ERROR", err.Error(), "")
			close(done)
			return
		}

		tokenPath := providers.GetTokenPath(providerName)
		if err := auth.EncryptToken(tokenPath, tokenData); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "<html><body>Erreur sauvegarde token : %v</body></html>", err)
			ipc.SendError("TOKEN_SAVE_ERROR", err.Error(), "")
			close(done)
			return
		}

		// Sauvegarde du provider sÃ©lectionnÃ© dans horizon_settings.json
		settings.Provider = providerName
		settingsPath := paths.GetSettingsPath()
		if b, err := json.MarshalIndent(settings, "", "  "); err == nil {
			_ = osWriteFileAtomic(settingsPath, b, 0644)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Connexion RÃ©ussie</title><style>body{background:#18181b;color:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}.card{background:#27272a;padding:40px;border-radius:12px;box-shadow:0 10px 25px rgba(0,0,0,0.5);text-align:center;max-width:400px}h2{color:#22c55e;margin-top:0}p{color:#a1a1aa;line-height:1.5}</style></head><body><div class="card"><h2>Connexion rÃ©ussie !</h2><p>Votre compte Cloud a Ã©tÃ© associÃ© avec succÃ¨s Ã  Gens Launcher.<br><br>Vous pouvez fermer cet onglet et continuer Ã  jouer.</p></div></body></html>`)

		ipc.Send(ipc.BaseMessage{
			Type:    "SUCCESS",
			Message: "Connexion rÃ©ussie !",
		})
		close(done)
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
		ipc.SendError("TIMEOUT", "DÃ©lai d'attente d'authentification dÃ©passÃ© (5 minutes).", "")
	}
}

func exchangeCodeForToken(providerName string, cfg config.ProviderConfig, code string) (auth.TokenData, error) {
	var tokenData auth.TokenData

	switch providerName {
	case "google":
		form := url.Values{
			"code":          {code},
			"client_id":     {cfg.ClientID},
			"client_secret": {cfg.ClientSecret},
			"redirect_uri":  {cfg.RedirectURI},
			"grant_type":    {"authorization_code"},
		}
		resp, err := http.PostForm(cfg.TokenURI, form)
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
		resp, err := http.PostForm(cfg.TokenURI, form)
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
		if cfg.ClientSecret != "" {
			form.Set("client_secret", cfg.ClientSecret)
		}
		resp, err := http.PostForm(cfg.TokenURI, form)
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
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, data, os.FileMode(perm)); err != nil {
		return err
	}
	return os.Rename(tmp, filePath)
}
