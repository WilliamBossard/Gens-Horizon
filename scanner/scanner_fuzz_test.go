package scanner

import (
	"encoding/json"
	"testing"
)

func FuzzFlexibleInt64UnmarshalJSON(f *testing.F) {
	seeds := [][]byte{
		[]byte(`0`),
		[]byte(`1790268044168`),
		[]byte(`1790268044168.8877`),
		[]byte(`"1790268044168"`),
		[]byte(`"1790268044168.5"`),
		[]byte(`null`),
		[]byte(`""`),
		[]byte(`-42`),
		[]byte(`"not-a-number"`),
		[]byte(`{}`),
		[]byte(`[]`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var val FlexibleInt64
		if err := val.UnmarshalJSON(raw); err == nil {
			out, mErr := json.Marshal(val)
			if mErr != nil {
				t.Fatalf("MarshalJSON failed after successful UnmarshalJSON(%q): %v", raw, mErr)
			}
			var roundTrip FlexibleInt64
			if rtErr := roundTrip.UnmarshalJSON(out); rtErr != nil {
				t.Fatalf("round-trip UnmarshalJSON(%q) failed: %v", out, rtErr)
			}
		}
	})
}
