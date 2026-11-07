package providers

import (
	"net"
	"net/http"
	"time"
)

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
	ExpectContinueTimeout: 1 * time.Second,
}

// NewPooledClient retourne un client HTTP configuré avec le transport mutualisé et le timeout spécifié.
func NewPooledClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: defaultPooledTransport,
		Timeout:   timeout,
	}
}
