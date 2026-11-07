package retry

import (
	"context"
	"math/rand"
	"net"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/ipc"
)

type Options struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Label      string
}

func DefaultOptions() Options {
	return Options{
		MaxRetries: 3,
		BaseDelay:  1500 * time.Millisecond,
		MaxDelay:   30 * time.Second,
		Label:      "Operation",
	}
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return e.Message
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if httpErr, ok := err.(*HTTPError); ok {
		// 429 Too Many Requests, ou erreurs serveur 5xx
		if httpErr.StatusCode == 429 || (httpErr.StatusCode >= 500 && httpErr.StatusCode <= 599) {
			return true
		}
		return false
	}
	return true // Erreurs réseau / timeouts par défaut
}

func Do(ctx context.Context, opts Options, fn func() error) error {
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = 1500 * time.Millisecond
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = 30 * time.Second
	}

	delay := opts.BaseDelay
	for attempt := 1; attempt <= opts.MaxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		if attempt == opts.MaxRetries || !IsRetryable(err) {
			return err
		}

		// Backoff exponentiel avec jitter (+- 20%)
		jitter := time.Duration(rand.Int63n(int64(delay / 5)))
		actualDelay := delay + jitter
		if actualDelay > opts.MaxDelay {
			actualDelay = opts.MaxDelay
		}

		if opts.Label != "" {
			ipc.Logf("[retry] %s : tentative %d/%d échouée (%v). Nouvelle tentative dans %v...", opts.Label, attempt, opts.MaxRetries, err, actualDelay)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(actualDelay):
		}

		delay *= 2
	}

	return nil
}

// CheckConnectivity effectue une résolution DNS concurrente ultra-rapide (similaire à Node.js dns.lookup)
func CheckConnectivity() bool {
	hosts := []string{"google.com", "cloudflare.com", "microsoft.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	success := make(chan struct{}, 1)
	done := make(chan struct{}, len(hosts))

	for _, host := range hosts {
		go func(h string) {
			ips, err := net.DefaultResolver.LookupHost(ctx, h)
			if err == nil && len(ips) > 0 {
				select {
				case success <- struct{}{}:
				default:
				}
			}
			done <- struct{}{}
		}(host)
	}

	for i := 0; i < len(hosts); i++ {
		select {
		case <-success:
			return true
		case <-done:
		}
	}
	return false
}

