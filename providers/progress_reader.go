package providers

import "io"

// ProgressReader enveloppe un io.Reader pour émettre en continu le pourcentage d'avancement global
type ProgressReader struct {
	Reader     io.Reader
	TotalSize  int64
	Processed  *int64
	LastPct    *int
	OnProgress func(pct int)
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 && pr.Processed != nil {
		*pr.Processed += int64(n)
		if pr.OnProgress != nil && pr.TotalSize > 0 && pr.LastPct != nil {
			pct := int((*pr.Processed * 100) / pr.TotalSize)
			if pct > 100 {
				pct = 100
			}
			if pct != *pr.LastPct && (pct >= *pr.LastPct+1 || pct == 100) {
				pr.OnProgress(pct)
				*pr.LastPct = pct
			}
		}
	}
	return n, err
}
