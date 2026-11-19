package operations

import "testing"

func TestMatchDeltaForInstance_NoPrefixCollision(t *testing.T) {
	ts, ok := MatchDeltaForInstance("GensHorizon_Delta_Minecraft_1_21_1700000000000.zip", "Minecraft_1_21")
	if !ok || ts != 1700000000000 {
		t.Fatalf("expected match for Minecraft_1_21 with ts=1700000000000, got ok=%v ts=%d", ok, ts)
	}

	if _, ok := MatchDeltaForInstance("GensHorizon_Delta_Minecraft_1_21_Fabric_1700000000000.zip", "Minecraft_1_21"); ok {
		t.Fatalf("unexpected prefix collision: Minecraft_1_21_Fabric delta matched Minecraft_1_21")
	}

	ts2, ok2 := MatchDeltaForInstance("GensHorizon_Delta_Minecraft_1_21_Fabric_1700000000000.zip", "Minecraft_1_21_Fabric")
	if !ok2 || ts2 != 1700000000000 {
		t.Fatalf("expected match for Minecraft_1_21_Fabric, got ok=%v ts=%d", ok2, ts2)
	}

	if _, _, ok := ParseDeltaFileName("GensHorizon_Delta_NoTimestamp.zip"); ok {
		t.Fatalf("expected invalid delta filename without numeric timestamp to fail")
	}
}
