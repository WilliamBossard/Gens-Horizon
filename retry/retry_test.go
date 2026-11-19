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

