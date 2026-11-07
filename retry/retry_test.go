package retry

import (
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
