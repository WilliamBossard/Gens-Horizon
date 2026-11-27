package providers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/retry"
)

var baseProcessCtx atomic.Pointer[context.Context]

// SetBaseContext configure le contexte racine du processus (annulé sur SIGINT/SIGTERM/SHUTDOWN)
// afin que les transferts HTTP en cours puissent être interrompus proprement.
func SetBaseContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	baseProcessCtx.Store(&ctx)
}

func baseContext() context.Context {
	if ptr := baseProcessCtx.Load(); ptr != nil && *ptr != nil {
		return *ptr
	}
	return context.Background()
}

// sanitizeHTTPBody tronque et assainit un corps d'erreur HTTP pour éviter d'exposer
// des payloads volumineux ou des caractères de contrôle dans les logs / messages IPC.
func sanitizeHTTPBody(body []byte) string {
	const maxLen = 512
	s := strings.TrimSpace(string(body))
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > maxLen {
		return s[:maxLen] + "...(tronqué)"
	}
	return s
}

// newProviderHTTPError construit une retry.HTTPError en extrayant systématiquement Retry-After
// et en assainissant le corps de réponse.
func newProviderHTTPError(prefix string, resp *http.Response, body []byte) *retry.HTTPError {
	return &retry.HTTPError{
		StatusCode: resp.StatusCode,
		RetryAfter: retry.ParseRetryAfter(resp.Header.Get("Retry-After")),
		Message:    fmt.Sprintf("%s (HTTP %d) : %s", prefix, resp.StatusCode, sanitizeHTTPBody(body)),
	}
}

// defaultPooledTransport est un transport HTTP mutualisé entre tous les providers Cloud
// optimisé pour le débit élevé, la réutilisation des sockets TLS et HTTP/2.
var defaultPooledTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10, // Au lieu du défaut Go de 2 qui causait des renégociations TLS constantes
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// NewPooledClient retourne un client HTTP configuré avec le transport mutualisé et le timeout spécifié.
func NewPooledClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: defaultPooledTransport,
		Timeout:   timeout,
	}
}

// clientWithTimeout réutilise le Transport déjà configuré sur base (notamment pour les tests/mocks)
// tout en appliquant le timeout voulu, ou retombe sur NewPooledClient.
func clientWithTimeout(base *http.Client, timeout time.Duration) *http.Client {
	if base != nil && base.Transport != nil {
		return &http.Client{
			Transport: base.Transport,
			Timeout:   timeout,
		}
	}
	return NewPooledClient(timeout)
}

// streamingClient retourne un client sans Timeout global sur le corps de réponse
// (les téléchargements longs sont protégés par un timeout d'inactivité côté lecture).
func streamingClient(base *http.Client) *http.Client {
	return clientWithTimeout(base, 0)
}

const uploadIdleTimeout = 90 * time.Second

type idleActivityReader struct {
	r     io.Reader
	timer *time.Timer
	idle  time.Duration
}

func (ir *idleActivityReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 && ir.timer != nil {
		ir.timer.Reset(ir.idle)
	}
	return n, err
}

// doUploadWithIdleTimeoutWithContext exécute une requête d'upload en surveillant l'activité I/O
// et en respectant l'annulation du contexte fourni.
func doUploadWithIdleTimeoutWithContext(parentCtx context.Context, client *http.Client, method, rawURL string, body io.Reader, contentLen int64, setHeaders func(*http.Request)) (*http.Response, func(), error) {
	if parentCtx == nil {
		parentCtx = baseContext()
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if client != nil && client.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parentCtx, client.Timeout)
	} else {
		ctx, cancel = context.WithCancel(parentCtx)
	}
	var timer *time.Timer
	reqBody := body
	if body != nil && uploadIdleTimeout > 0 {
		timer = time.AfterFunc(uploadIdleTimeout, cancel)
		reqBody = &idleActivityReader{r: body, timer: timer, idle: uploadIdleTimeout}
	}
	cleanup := func() {
		if timer != nil {
			timer.Stop()
		}
		cancel()
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	req.ContentLength = contentLen
	if setHeaders != nil {
		setHeaders(req)
	}

	resp, err := streamingClient(client).Do(req)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if timer != nil {
		timer.Stop()
	}
	return resp, cleanup, nil
}

