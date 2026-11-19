package retry

import (
	"fmt"
	"testing"
)

func TestIsRetryableUnwrapsWrappedHTTPError(t *testing.T) {
	err404 := fmt.Errorf("provider call failed: %w", &HTTPError{StatusCode: 404, Message: "not found"})
	if IsRetryable(err404) {
		t.Fatalf("wrapped HTTP 404 must not be retryable")
	}

	err429 := fmt.Errorf("provider call failed: %w", &HTTPError{StatusCode: 429, Message: "rate limit"})
	if !IsRetryable(err429) {
		t.Fatalf("wrapped HTTP 429 must be retryable")
	}

	err503 := fmt.Errorf("provider call failed: %w", &HTTPError{StatusCode: 503, Message: "unavailable"})
	if !IsRetryable(err503) {
		t.Fatalf("wrapped HTTP 503 must be retryable")
	}
}
