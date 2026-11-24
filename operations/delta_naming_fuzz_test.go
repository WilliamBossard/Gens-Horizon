package operations

import "testing"

func FuzzMatchDeltaForInstance(f *testing.F) {
	f.Add("GensHorizon_Delta_survival_1700000000000.zip", "survival")
	f.Add("GensHorizon_Delta_survival_2_1700000000000.zip", "survival")
	f.Add("GensHorizon_Delta_my_pack_123456.zip", "my_pack")
	f.Add("GensHorizon_Backup_survival.zip", "survival")
	f.Add("", "survival")
	f.Add("GensHorizon_Delta_survival_abc.zip", "survival")

	f.Fuzz(func(t *testing.T, fileName, safeInst string) {
		ts, ok := MatchDeltaForInstance(fileName, safeInst)
		if ok && ts <= 0 {
			t.Fatalf("MatchDeltaForInstance(%q, %q) returned ok=true with non-positive ts=%d", fileName, safeInst, ts)
		}
	})
}
