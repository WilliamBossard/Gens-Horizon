package retry

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
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

// FromSettings convertit les réglages Horizon en Options de retry.
// Contrairement au zéro-value de Options (qui garde 3 tentatives par défaut),
// un maxRetries <= 0 issu des réglages explicites désactive les réessais (1 seule tentative).
func FromSettings(maxRetries int, baseDelayMs int, label string) Options {
	effRetries := maxRetries
	if effRetries <= 0 {
		effRetries = -1
	}
	return Options{
		MaxRetries: effRetries,
		BaseDelay:  time.Duration(baseDelayMs) * time.Millisecond,
		Label:      label,
	}
}

type HTTPError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return e.Message
}

// ParseRetryAfter parse un en-tête Retry-After (secondes ou date HTTP) en durée bornée à 120s.
func ParseRetryAfter(val string) time.Duration {
	v := strings.TrimSpace(val)
	if v == "" {
		return 0
	}
	const maxRetryAfter = 120 * time.Second
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		d := time.Duration(secs) * time.Second
		if d > maxRetryAfter {
			return maxRetryAfter
		}
		return d
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0
		}
		if d > maxRetryAfter {
			return maxRetryAfter
		}
		return d
	}
	return 0
}

type nonRetryableError interface {
	NonRetryable() bool
}

type permanentError struct {
	err error
}

func (p permanentError) Error() string        { return p.err.Error() }
func (p permanentError) Unwrap() error        { return p.err }
func (p permanentError) NonRetryable() bool   { return true }

// Permanent marque une erreur comme non-retryable pour retry.Do.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var nr nonRetryableError
	if errors.As(err, &nr) && nr.NonRetryable() {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var syntaxErr *json.SyntaxError
	var unmarshalErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &unmarshalErr) {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == 429 || (httpErr.StatusCode >= 500 && httpErr.StatusCode <= 599) {
			return true
		}
		return false
	}
	return true
}

func Do(ctx context.Context, opts Options, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.MaxRetries < 0 {
		opts.MaxRetries = 1
	} else if opts.MaxRetries == 0 {
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
		var jitter time.Duration
		if jitterRange := int64(delay / 5); jitterRange > 0 {
			jitter = time.Duration(rand.Int63n(jitterRange))
		}
		actualDelay := delay + jitter
		if actualDelay > opts.MaxDelay {
			actualDelay = opts.MaxDelay
		}
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.RetryAfter > actualDelay {
			actualDelay = httpErr.RetryAfter
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
	for _, envKey := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if strings.TrimSpace(os.Getenv(envKey)) != "" {
			return true
		}
	}
	return false
}

