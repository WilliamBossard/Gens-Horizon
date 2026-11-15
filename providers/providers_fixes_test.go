package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/auth"
	"github.com/WilliamBossard/Gens-Horizon/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(status int, payload any) *http.Response {
	b, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func TestDownloadMultiRangeRejects200OnNonZeroChunk(t *testing.T) {
	const testSize = int64(64 * 1024)
	testData := bytes.Repeat([]byte("A"), int(testSize))

	tmpDir, err := os.MkdirTemp("", "horizon_reject200_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dest := filepath.Join(tmpDir, "corrupt_guard.bin")
	err = downloadMultiRange(dest, testSize, nil, func(start, end int64) (*http.Response, error) {
		if start == 0 {
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, testSize)}},
				Body:       io.NopCloser(bytes.NewReader(testData[start : end+1])),
			}, nil
		}
		// Serveur bogué renvoyant 200 OK avec le fichier entier pour un segment > 0
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(testData)),
		}, nil
	})

	if err == nil {
		t.Fatalf("expected downloadMultiRange to reject 200 OK on chunk >= 1, got nil")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected partial destination file %s to be removed on failure", dest)
	}
}

type failingReaderAfter struct {
	data    []byte
	offset  int
	failErr error
}

func (f *failingReaderAfter) Read(p []byte) (int, error) {
	if f.offset >= len(f.data) {
		return 0, f.failErr
	}
	n := copy(p, f.data[f.offset:])
	f.offset += n
	return n, nil
}

func TestDownloadMultiRangeRetriesChunk0EOF(t *testing.T) {
	const testSize = int64(64 * 1024)
	testData := make([]byte, testSize)
	for i := range testData {
		testData[i] = byte(i%251 + 1)
	}

	tmpDir, err := os.MkdirTemp("", "horizon_chunk0_retry_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dest := filepath.Join(tmpDir, "retry_chunk0.bin")
	var chunk0Calls atomic.Int32

	err = downloadMultiRange(dest, testSize, nil, func(start, end int64) (*http.Response, error) {
		if end < testSize-1 {
			call := chunk0Calls.Add(1)
			if call == 1 {
				half := (end + 1) / 2
				return &http.Response{
					StatusCode: http.StatusPartialContent,
					Body: io.NopCloser(&failingReaderAfter{
						data:    testData[:half],
						failErr: io.ErrUnexpectedEOF,
					}),
				}, nil
			}
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, testSize)}},
			Body:       io.NopCloser(bytes.NewReader(testData[start : end+1])),
		}, nil
	})
	if err != nil {
		t.Fatalf("downloadMultiRange failed: %v", err)
	}
	if chunk0Calls.Load() < 2 {
		t.Fatalf("expected chunk 0 to be retried after initial EOF")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(got, testData) {
		t.Fatalf("downloaded bytes do not match original")
	}
}

func TestDropboxChunkedUploadRefreshes401Token(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_dropbox_401_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	oldChunk := dropboxChunkSize
	dropboxChunkSize = 16
	defer func() { dropboxChunkSize = oldChunk }()

	payload := []byte("0123456789ABCDEF0123456789abcdefFINALEND") // 40 bytes -> 16 + 16 + 8
	srcPath := filepath.Join(tmpDir, "test.zip")
	if err := os.WriteFile(srcPath, payload, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var mu sync.Mutex
	var uploaded bytes.Buffer
	var append401Once atomic.Bool
	var finishAuth string

	d := &DropboxProvider{
		tokenData: auth.TokenData{
			AccessToken:  "old_dbx_tok",
			RefreshToken: "ref_dbx_tok",
			ExpiryDate:   time.Now().Add(time.Hour).UnixMilli(),
		},
		tokenPath: filepath.Join(tmpDir, "token_dropbox.json"),
		creds:     config.ProviderConfig{ClientID: "cid", ClientSecret: "csec"},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case "https://api.dropboxapi.com/oauth2/token":
					return jsonResponse(http.StatusOK, map[string]any{
						"access_token": "new_dbx_tok",
						"expires_in":   3600,
					}), nil
				case "https://content.dropboxapi.com/2/files/upload_session/start":
					chunk, _ := io.ReadAll(r.Body)
					mu.Lock()
					uploaded.Write(chunk)
					mu.Unlock()
					return jsonResponse(http.StatusOK, map[string]any{"session_id": "sess-1"}), nil
				case "https://content.dropboxapi.com/2/files/upload_session/append_v2":
					if append401Once.CompareAndSwap(false, true) {
						return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "expired"}), nil
					}
					if r.Header.Get("Authorization") != "Bearer new_dbx_tok" {
						return nil, fmt.Errorf("expected refreshed token on append retry, got %s", r.Header.Get("Authorization"))
					}
					chunk, _ := io.ReadAll(r.Body)
					mu.Lock()
					uploaded.Write(chunk)
					mu.Unlock()
					return jsonResponse(http.StatusOK, map[string]any{}), nil
				case "https://content.dropboxapi.com/2/files/upload_session/finish":
					finishAuth = r.Header.Get("Authorization")
					chunk, _ := io.ReadAll(r.Body)
					mu.Lock()
					uploaded.Write(chunk)
					mu.Unlock()
					return jsonResponse(http.StatusOK, map[string]any{"id": "id:done-40"}), nil
				default:
					return nil, fmt.Errorf("unexpected URL: %s", r.URL.String())
				}
			}),
		},
	}

	id, err := d.UploadZip("test.zip", srcPath, "", nil)
	if err != nil {
		t.Fatalf("UploadZip failed: %v", err)
	}
	if id != "id:done-40" {
		t.Errorf("unexpected file id: %s", id)
	}
	if finishAuth != "Bearer new_dbx_tok" {
		t.Errorf("expected finish to use refreshed token, got %q", finishAuth)
	}
	if !bytes.Equal(uploaded.Bytes(), payload) {
		t.Errorf("uploaded payload mismatch: got %q, want %q", uploaded.String(), string(payload))
	}
}

func TestOneDriveSimpleUploadRewindsBodyOn401(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_od_rewind_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	payload := []byte("OneDriveSimpleUploadPayloadData_1234567890")
	srcPath := filepath.Join(tmpDir, "small.zip")
	if err := os.WriteFile(srcPath, payload, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var putAttempt atomic.Int32
	var receivedAfter401 []byte

	o := &OneDriveProvider{
		tokenData: auth.TokenData{
			AccessToken:  "old_od_tok",
			RefreshToken: "ref_od_tok",
			ExpiryDate:   time.Now().Add(time.Hour).UnixMilli(),
		},
		tokenPath: filepath.Join(tmpDir, "token_onedrive.json"),
		creds:     config.ProviderConfig{ClientID: "od-cid"},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == "https://login.microsoftonline.com/common/oauth2/v2.0/token" {
					return jsonResponse(http.StatusOK, map[string]any{
						"access_token": "new_od_tok",
						"expires_in":   3600,
					}), nil
				}
				attempt := putAttempt.Add(1)
				body, _ := io.ReadAll(r.Body)
				if attempt == 1 {
					return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "expired"}), nil
				}
				if r.Header.Get("Authorization") != "Bearer new_od_tok" {
					return nil, fmt.Errorf("expected Bearer new_od_tok on retry, got %q", r.Header.Get("Authorization"))
				}
				receivedAfter401 = body
				return jsonResponse(http.StatusCreated, map[string]any{"id": "od-item-1"}), nil
			}),
		},
	}

	id, err := o.UploadZip("small.zip", srcPath, "", nil)
	if err != nil {
		t.Fatalf("OneDrive UploadZip failed: %v", err)
	}
	if id != "od-item-1" {
		t.Errorf("unexpected id: %s", id)
	}
	if !bytes.Equal(receivedAfter401, payload) {
		t.Errorf("expected rewound full payload %q, got %q", string(payload), string(receivedAfter401))
	}
}

func TestGoogleDownloadFileUsesRefreshedTokenForAbuseConfirm(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_google_abuse_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	payload := []byte("RealZipBinaryPayload")
	var abuseAuthHeader string

	g := &GoogleProvider{
		tokenData: auth.TokenData{
			AccessToken:  "old_g_tok",
			RefreshToken: "ref_g_tok",
			ExpiryDate:   time.Now().Add(time.Hour).UnixMilli(),
		},
		tokenPath: filepath.Join(tmpDir, "token_google.json"),
		creds:     config.ProviderConfig{ClientID: "g-cid", ClientSecret: "g-csec"},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == "https://oauth2.googleapis.com/token" {
					return jsonResponse(http.StatusOK, map[string]any{
						"access_token": "new_g_tok",
						"expires_in":   3600,
					}), nil
				}
				if strings.Contains(r.URL.RawQuery, "acknowledgeAbuse=true") {
					abuseAuthHeader = r.Header.Get("Authorization")
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/zip"}},
						Body:       io.NopCloser(bytes.NewReader(payload)),
					}, nil
				}
				if r.Header.Get("Authorization") == "Bearer old_g_tok" {
					return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "expired"}), nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader("<html>Virus scan warning</html>")),
				}, nil
			}),
		},
	}

	dest := filepath.Join(tmpDir, "out.zip")
	if err := g.DownloadFile("file-123", dest, nil, int64(len(payload))); err != nil {
		t.Fatalf("Google DownloadFile failed: %v", err)
	}
	if abuseAuthHeader != "Bearer new_g_tok" {
		t.Fatalf("expected acknowledgeAbuse request to use refreshed token, got %q", abuseAuthHeader)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %q, want %q", string(got), string(payload))
	}
}

func TestGoogleListFilesTruncatedError(t *testing.T) {
	g := &GoogleProvider{
		tokenData: auth.TokenData{
			AccessToken: "valid_tok",
			ExpiryDate:  time.Now().Add(time.Hour).UnixMilli(),
		},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, map[string]any{
					"nextPageToken": "always-more",
					"files":         []any{},
				}), nil
			}),
		},
	}

	_, err := g.ListFiles("")
	if !errors.Is(err, ErrListingTruncated) {
		t.Fatalf("expected ErrListingTruncated, got %v", err)
	}
}

func TestGoogleProviderUploadZipUsesInjectedClientTransport(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "payload.zip")
	if err := os.WriteFile(tmpFile, []byte("PK-TEST-DATA"), 0644); err != nil {
		t.Fatal(err)
	}

	var putCalled atomic.Bool
	g := &GoogleProvider{
		tokenData: auth.TokenData{
			AccessToken: "valid_tok",
			ExpiryDate:  time.Now().Add(time.Hour).UnixMilli(),
		},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost && strings.Contains(r.URL.Host, "googleapis.com") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Location": []string{"https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&upload_id=abc"}},
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				}
				if r.Method == http.MethodPut && strings.Contains(r.URL.RawQuery, "upload_id=abc") {
					putCalled.Store(true)
					return jsonResponse(http.StatusOK, map[string]any{"id": "uploaded-id-1"}), nil
				}
				return jsonResponse(http.StatusBadRequest, map[string]any{"error": "unexpected"}), nil
			}),
		},
	}

	id, err := g.UploadZip("test.zip", tmpFile, "", nil)
	if err != nil {
		t.Fatalf("UploadZip failed: %v", err)
	}
	if !putCalled.Load() {
		t.Fatalf("expected GoogleProvider.UploadZip to use injected Transport for chunk PUT")
	}
	if id != "uploaded-id-1" {
		t.Fatalf("expected uploaded-id-1, got %q", id)
	}
}

func TestProvidersImplementFileRenamerAndOneDriveSimpleUploadRetries401(t *testing.T) {
	var _ FileRenamer = (*GoogleProvider)(nil)
	var _ FileRenamer = (*DropboxProvider)(nil)
	var _ FileRenamer = (*OneDriveProvider)(nil)

	tmpFile := filepath.Join(t.TempDir(), "small.zip")
	if err := os.WriteFile(tmpFile, []byte("PK-SMALL"), 0644); err != nil {
		t.Fatal(err)
	}

	var putAttempts atomic.Int32
	o := &OneDriveProvider{
		tokenData: auth.TokenData{
			AccessToken:  "expired_tok",
			RefreshToken: "ref_tok",
			ExpiryDate:   time.Now().Add(time.Hour).UnixMilli(),
		},
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Host, "login.microsoftonline.com") {
					return jsonResponse(http.StatusOK, map[string]any{
						"access_token":  "fresh_tok",
						"refresh_token": "ref_tok",
						"expires_in":    3600,
					}), nil
				}
				if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/approot:/small.zip:/content") {
					n := putAttempts.Add(1)
					if n == 1 {
						return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "expired"}), nil
					}
					if r.Header.Get("Authorization") != "Bearer fresh_tok" {
						return jsonResponse(http.StatusBadRequest, map[string]any{"error": "missing fresh token"}), nil
					}
					return jsonResponse(http.StatusCreated, map[string]any{"id": "od-small-1"}), nil
				}
				return jsonResponse(http.StatusBadRequest, map[string]any{"error": "unexpected"}), nil
			}),
		},
	}

	id, err := o.UploadZip("small.zip", tmpFile, "", nil)
	if err != nil {
		t.Fatalf("OneDrive UploadZip failed: %v", err)
	}
	if id != "od-small-1" || putAttempts.Load() != 2 {
		t.Fatalf("expected id=od-small-1 after 2 attempts, got id=%q attempts=%d", id, putAttempts.Load())
	}
}

func TestDropboxListFilesEnforcesMaxPagesCap(t *testing.T) {
	d := &DropboxProvider{
		tokenData: auth.TokenData{
			AccessToken:  "dbx_tok",
			RefreshToken: "dbx_ref",
			ExpiryDate:   time.Now().Add(time.Hour).UnixMilli(),
		},
		creds: config.ProviderConfig{ClientID: "cid", ClientSecret: "csec"},
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, map[string]any{
					"entries":  []any{},
					"cursor":   "infinite-cursor",
					"has_more": true,
				}), nil
			}),
		},
	}

	_, err := d.ListFiles("")
	if !errors.Is(err, ErrListingTruncated) {
		t.Fatalf("expected ErrListingTruncated on infinite Dropbox cursor, got: %v", err)
	}
}

func TestGoogleUploadJSONEscapesExistingID(t *testing.T) {
	var capturedPath string
	g := &GoogleProvider{
		tokenData: auth.TokenData{
			AccessToken: "tok",
			ExpiryDate:  time.Now().Add(time.Hour).UnixMilli(),
		},
		creds: config.ProviderConfig{ClientID: "cid", ClientSecret: "csec"},
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				capturedPath = r.URL.EscapedPath()
				return jsonResponse(http.StatusOK, map[string]any{"id": "escaped-ok"}), nil
			}),
		},
	}

	id, err := g.UploadJSON("meta.json", map[string]string{"k": "v"}, "file/id?x#1")
	if err != nil {
		t.Fatalf("UploadJSON failed: %v", err)
	}
	if id != "escaped-ok" {
		t.Fatalf("unexpected id: %q", id)
	}
	wantSuffix := "/files/file%2Fid%3Fx%231"
	if !strings.HasSuffix(capturedPath, wantSuffix) {
		t.Fatalf("expected escaped path ending with %q, got %q", wantSuffix, capturedPath)
	}
}

func TestMarshalDropboxAPIArgEscapesNonASCII(t *testing.T) {
	encoded, err := marshalDropboxAPIArg(map[string]string{"path": "/Été Moddé/pack 😀.zip"})
	if err != nil {
		t.Fatalf("marshalDropboxAPIArg failed: %v", err)
	}
	for i := 0; i < len(encoded); i++ {
		if encoded[i] >= 0x80 {
			t.Fatalf("Dropbox-API-Arg contains non-ASCII byte 0x%02x at %d: %s", encoded[i], i, encoded)
		}
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if decoded["path"] != "/Été Moddé/pack 😀.zip" {
		t.Fatalf("round-trip mismatch: got %q", decoded["path"])
	}
}

func TestDownloadSmartFallbackWhenSubsequentChunkReturns200(t *testing.T) {
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "fallback.bin")
	payload := bytes.Repeat([]byte("ABCDEFGH"), 32*1024) // 256 KiB

	var calls atomic.Int32
	err := downloadMultiRange(dest, int64(len(payload)), nil, func(start, end int64) (*http.Response, error) {
		n := calls.Add(1)
		if n == 1 {
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload))}},
				Body:       io.NopCloser(bytes.NewReader(payload[start : end+1])),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(payload)),
		}, nil
	})
	if !errors.Is(err, errMultiRangeFallback) {
		t.Fatalf("expected errMultiRangeFallback when chunk > 0 returns 200 OK, got: %v", err)
	}
}

func TestDownloadMultiRangeFallbackDeterministicUnderCancel(t *testing.T) {
	tmpDir := t.TempDir()
	// 25 MiB virtuels -> 2 chunks; chunk 0 bloque sur un slow reader, chunk 1 reçoit 200 OK et annule le contexte
	const virtualSize = int64(25 * 1024 * 1024)
	for run := 0; run < 5; run++ {
		dest := filepath.Join(tmpDir, fmt.Sprintf("race_%d.bin", run))
		err := downloadMultiRange(dest, virtualSize, nil, func(start, end int64) (*http.Response, error) {
			if start == 0 {
				pr, pw := io.Pipe()
				go func() {
					_, _ = pw.Write(bytes.Repeat([]byte("A"), 4096))
					time.Sleep(150 * time.Millisecond)
					_ = pw.Close()
				}()
				return &http.Response{
					StatusCode: http.StatusPartialContent,
					Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, virtualSize)}},
					Body:       pr,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte("fallback"))),
			}, nil
		})
		if !errors.Is(err, errMultiRangeFallback) {
			t.Fatalf("run %d: expected deterministic errMultiRangeFallback even under concurrent chunk cancel, got: %v", run, err)
		}
	}
}

func TestProvidersImplementFileTimestampFetcher(t *testing.T) {
	wantTime := time.Date(2026, 11, 15, 14, 30, 0, 0, time.UTC)

	gp := &GoogleProvider{
		tokenData: auth.TokenData{AccessToken: "g-tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "missing") {
					return jsonResponse(http.StatusNotFound, map[string]any{}), nil
				}
				return jsonResponse(http.StatusOK, map[string]any{"modifiedTime": wantTime.Format(time.RFC3339)}), nil
			}),
		},
	}
	var _ FileTimestampFetcher = gp
	gt, err := gp.GetFileModifiedTime("file-1")
	if err != nil || !gt.Equal(wantTime) {
		t.Fatalf("Google GetFileModifiedTime failed: got %v, err=%v", gt, err)
	}
	if _, err := gp.GetFileModifiedTime("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Google expected ErrNotFound on 404, got %v", err)
	}

	op := &OneDriveProvider{
		tokenData: auth.TokenData{AccessToken: "od-tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, map[string]any{"lastModifiedDateTime": wantTime.Format(time.RFC3339)}), nil
			}),
		},
	}
	var _ FileTimestampFetcher = op
	ot, err := op.GetFileModifiedTime("od-1")
	if err != nil || !ot.Equal(wantTime) {
		t.Fatalf("OneDrive GetFileModifiedTime failed: got %v, err=%v", ot, err)
	}

	dp := &DropboxProvider{
		tokenData: auth.TokenData{AccessToken: "db-tok", ExpiryDate: time.Now().Add(time.Hour).UnixMilli()},
		client: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, map[string]any{"server_modified": wantTime.Format(time.RFC3339)}), nil
			}),
		},
	}
	var _ FileTimestampFetcher = dp
	dt, err := dp.GetFileModifiedTime("id:abc")
	if err != nil || !dt.Equal(wantTime) {
		t.Fatalf("Dropbox GetFileModifiedTime failed: got %v, err=%v", dt, err)
	}
}
