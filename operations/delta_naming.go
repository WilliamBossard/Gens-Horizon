package operations

import (
	"strconv"
	"strings"
)

// ParseDeltaFileName extrait strictement le slug d'instance et l'horodatage numérique
// d'un nom d'archive delta de la forme "GensHorizon_Delta_<slug>_<timestamp>.zip".
// Contrairement à strings.HasPrefix, cette fonction empêche toute collision lorsqu'un
// slug d'instance est préfixe d'un autre (ex. "Minecraft_1_21" vs "Minecraft_1_21_Fabric").
func ParseDeltaFileName(name string) (slug string, ts int64, ok bool) {
	if !strings.HasPrefix(name, PrefixDelta) || !strings.HasSuffix(name, ".zip") {
		return "", 0, false
	}
	stripped := strings.TrimSuffix(strings.TrimPrefix(name, PrefixDelta), ".zip")
	idx := strings.LastIndex(stripped, "_")
	if idx <= 0 || idx >= len(stripped)-1 {
		return "", 0, false
	}
	slugPart := stripped[:idx]
	rawTs := stripped[idx+1:]
	parsedTs, err := strconv.ParseInt(rawTs, 10, 64)
	if err != nil || parsedTs <= 0 {
		return "", 0, false
	}
	return slugPart, parsedTs, true
}

// MatchDeltaForInstance vérifie strictement qu'un nom d'archive delta appartient
// exactement à safeInst et retourne son horodatage numérique.
func MatchDeltaForInstance(name, safeInst string) (ts int64, ok bool) {
	slug, parsedTs, valid := ParseDeltaFileName(name)
	if !valid || slug != safeInst {
		return 0, false
	}
	return parsedTs, true
}
