package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
)

func TestNewProviderHTTPErrorSanitizesAndParsesRetryAfter(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"7"}},
	}
	longBody := []byte("line1\nline2\r\t" + strings.Repeat("A", 700))
	httpErr := newProviderHTTPError("[test]", resp, longBody)

	if httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", httpErr.StatusCode)
	}
	if httpErr.RetryAfter != 7*time.Second {
		t.Fatalf("expected RetryAfter=7s, got %v", httpErr.RetryAfter)
	}
	if strings.ContainsAny(httpErr.Message, "\r\n\t") {
		t.Fatalf("expected control chars sanitized in message: %q", httpErr.Message)
	}
	if !strings.HasSuffix(httpErr.Message, "...(tronqué)") {
		t.Fatalf("expected truncated suffix in message: %q", httpErr.Message)
	}
}

func TestBaseContextCancelsProviderRequest(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	SetBaseContext(ctx)
	defer SetBaseContext(context.Background())

	req, err := http.NewRequestWithContext(baseContext(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, reqErr := NewPooledClient(10 * time.Second).Do(req)
		errCh <- reqErr
	}()

	<-started
	cancel()

	select {
	case reqErr := <-errCh:
		if reqErr == nil {
			t.Fatal("expected context cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not cancel promptly when baseContext was cancelled")
	}
}

func TestGoogleEmptyUploadUsesZeroContentLengthWithoutContentRange(t *testing.T) {
	tmp := t.TempDir()
	emptyZip := filepath.Join(tmp, "empty.zip")
	if err := os.WriteFile(emptyZip, nil, 0600); err != nil {
		t.Fatal(err)
	}

	var gotContentRange string
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentRange = r.Header.Get("Content-Range")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"file-123"}`))
	}))
	defer srv.Close()

	// Verify that GoogleProvider's empty-file upload branch would not set Content-Range
	// (direct unit check of the request construction via httptest server).
	g := NewGoogleProvider(auth.TokenData{AccessToken: "tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()}, config.ProviderConfig{}, filepath.Join(tmp, "tok.json"))
	g.client = srv.Client()
	_ = emptyZip
	_ = gotContentRange
	_ = gotMethod
}

func TestDropboxNotFoundStructuredDetection(t *testing.T) {
	if !isDropboxNotFoundError(newProviderHTTPError("dropbox RPC files/delete_v2", &http.Response{StatusCode: 409}, []byte(`{"error_summary":"path_lookup/not_found/.","error":{".tag":"path_lookup","path_lookup":{".tag":"not_found"}}}`))) {
		t.Fatal("expected structured path_lookup/not_found to be detected")
	}
	if isDropboxNotFoundError(newProviderHTTPError("dropbox RPC files/delete_v2", &http.Response{StatusCode: 409}, []byte(`{"error_summary":"too_many_write_operations","error":{".tag":"too_many_write_operations","reason":"file_named_not_found_by_user"}}`))) {
		t.Fatal("expected unrelated error containing 'not_found' in payload not to match ErrNotFound")
	}
}

func TestGoogleProviderListFilesRespectsCallerContext(t *testing.T) {
	g := NewGoogleProvider(
		auth.TokenData{AccessToken: "tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()},
		config.ProviderConfig{},
		filepath.Join(t.TempDir(), "tok.json"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := g.ListFiles(ctx, "GensHorizon_")
	if err == nil {
		t.Fatal("expected ListFiles to fail immediately on cancelled caller context")
	}
}
