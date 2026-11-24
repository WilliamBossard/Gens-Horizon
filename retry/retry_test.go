package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckConnectivity(t *testing.T) {
	start := time.Now()
	online := CheckConnectivity()
	elapsed := time.Since(start)

	if !online {
		t.Log("Note: CheckConnectivity reported offline (may happen in offline test environment)")
	} else {
		t.Logf("CheckConnectivity succeeded in %v", elapsed)
	}
}

func TestFromSettingsZeroMeansNoRetry(t *testing.T) {
	var calls int
	err := Do(context.Background(), FromSettings(0, 1, "no-retry"), func() error {
		calls++
		return errors.New("transient")
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected FromSettings(0) to execute exactly 1 attempt (no retry), got %d", calls)
	}

	calls = 0
	_ = Do(context.Background(), Options{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, func() error {
		calls++
		return errors.New("transient")
	})
	if calls != 3 {
		t.Fatalf("expected zero-value Options to keep 3 default attempts, got %d", calls)
	}
}


func TestIsRetryableNonTransient(t *testing.T) {
	if IsRetryable(context.Canceled) {
		t.Fatalf("context.Canceled should not be retryable")
	}
	if IsRetryable(context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded should not be retryable")
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := Do(canceledCtx, Options{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, func() error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("expected immediate context.Canceled with 0 calls, got err=%v calls=%d", err, calls)
	}
}
