package providers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
	"github.com/WilliamBossard/Gens-Horizon/retry"
)

func rateLimitResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"1"}, "Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"rate_limited"}`)),
	}
}

func TestProvidersRetryOnHTTP429Burst(t *testing.T) {
	fastOpts := retry.Options{
		MaxRetries: 4,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   5 * time.Millisecond,
		Label:      "test-429",
	}

	t.Run("google", func(t *testing.T) {
		var calls atomic.Int32
		p := &GoogleProvider{
			tokenData: auth.TokenData{AccessToken: "tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()},
			creds:     config.ProviderConfig{ClientID: "id"},
			client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if calls.Add(1) <= 2 {
						return rateLimitResponse(), nil
					}
					return jsonResponse(http.StatusOK, map[string]any{
						"files": []map[string]any{
							{"id": "g1", "name": "GensHorizon_Backup_test.zip", "size": "123", "modifiedTime": time.Now().UTC().Format(time.RFC3339)},
						},
					}), nil
				}),
			},
		}
		var files []CloudFile
		err := retry.Do(context.Background(), fastOpts, func() error {
			var lErr error
			files, lErr = p.ListFiles(context.Background(), "GensHorizon_")
			return lErr
		})
		if err != nil {
			t.Fatalf("expected google 429 burst to recover, got: %v", err)
		}
		if len(files) != 1 || calls.Load() != 3 {
			t.Fatalf("unexpected files=%d calls=%d", len(files), calls.Load())
		}
	})

	t.Run("onedrive", func(t *testing.T) {
		var calls atomic.Int32
		p := &OneDriveProvider{
			tokenData: auth.TokenData{AccessToken: "tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()},
			creds:     config.ProviderConfig{ClientID: "id"},
			client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if calls.Add(1) <= 2 {
						return rateLimitResponse(), nil
					}
					return jsonResponse(http.StatusOK, map[string]any{
						"value": []map[string]any{
							{"id": "o1", "name": "GensHorizon_Backup_test.zip", "size": 456, "lastModifiedDateTime": time.Now().UTC().Format(time.RFC3339), "file": map[string]any{}},
						},
					}), nil
				}),
			},
		}
		var files []CloudFile
		err := retry.Do(context.Background(), fastOpts, func() error {
			var lErr error
			files, lErr = p.ListFiles(context.Background(), "GensHorizon_")
			return lErr
		})
		if err != nil {
			t.Fatalf("expected onedrive 429 burst to recover, got: %v", err)
		}
		if len(files) != 1 || calls.Load() != 3 {
			t.Fatalf("unexpected files=%d calls=%d", len(files), calls.Load())
		}
	})

	t.Run("dropbox", func(t *testing.T) {
		var calls atomic.Int32
		p := &DropboxProvider{
			tokenData: auth.TokenData{AccessToken: "tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()},
			creds:     config.ProviderConfig{ClientID: "id"},
			client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if calls.Add(1) <= 2 {
						return rateLimitResponse(), nil
					}
					return jsonResponse(http.StatusOK, map[string]any{
						"entries": []map[string]any{
							{".tag": "file", "id": "id:d1", "name": "GensHorizon_Backup_test.zip", "size": 789, "server_modified": time.Now().UTC().Format(time.RFC3339)},
						},
						"has_more": false,
					}), nil
				}),
			},
		}
		var files []CloudFile
		err := retry.Do(context.Background(), fastOpts, func() error {
			var lErr error
			files, lErr = p.ListFiles(context.Background(), "GensHorizon_")
			return lErr
		})
		if err != nil {
			t.Fatalf("expected dropbox 429 burst to recover, got: %v", err)
		}
		if len(files) != 1 || calls.Load() != 3 {
			t.Fatalf("unexpected files=%d calls=%d", len(files), calls.Load())
		}
	})
}
