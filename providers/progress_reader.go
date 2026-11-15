package providers

import (
	"io"
	"sync"
)

// ProgressReader enveloppe un io.Reader pour émettre en continu le pourcentage d'avancement global
type ProgressReader struct {
	Reader     io.Reader
	TotalSize  int64
	Processed  *int64
	LastPct    *int
	OnProgress func(pct int)
	mu         sync.Mutex
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 && pr.Processed != nil {
		pr.mu.Lock()
		*pr.Processed += int64(n)
		processed := *pr.Processed
		shouldNotify := false
		pct := 0
		if pr.OnProgress != nil && pr.TotalSize > 0 && pr.LastPct != nil {
			pct = int((processed * 100) / pr.TotalSize)
			if pct > 100 {
				pct = 100
			}
			if pct != *pr.LastPct && (pct >= *pr.LastPct+1 || pct == 100) {
				*pr.LastPct = pct
				shouldNotify = true
			}
		}
		pr.mu.Unlock()
		if shouldNotify {
			pr.OnProgress(pct)
		}
	}
	return n, err
}

