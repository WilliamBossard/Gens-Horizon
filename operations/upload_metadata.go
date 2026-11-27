package operations

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func computeFileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readFileBase64(filePath string) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	var mime string
	switch ext {
	case ".png":
		mime = "image/png"
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	case ".gif":
		mime = "image/gif"
	case ".bmp":
		mime = "image/bmp"
	default:
		return "", "", fmt.Errorf("format d'icône non autorisé : %s", ext)
	}
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(bytes), mime, nil
}

func isPathStrictlyUnder(baseDir, targetPath string) bool {
	absBase, err1 := filepath.Abs(filepath.Clean(baseDir))
	absTarget, err2 := filepath.Abs(filepath.Clean(targetPath))
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func isRealPathStrictlyUnder(baseDir, targetPath string) bool {
	if !isPathStrictlyUnder(baseDir, targetPath) {
		return false
	}
	info, err := os.Lstat(targetPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return false
	}
	realBase, err1 := filepath.EvalSymlinks(baseDir)
	realTarget, err2 := filepath.EvalSymlinks(targetPath)
	if err1 != nil || err2 != nil {
		return false
	}
	return isPathStrictlyUnder(realBase, realTarget)
}

var validImageDataURIRe = regexp.MustCompile(`^data:image/(?:png|jpeg|jpg|webp|gif|bmp);base64,[A-Za-z0-9+/=]+$`)
var validSvgDataURIRe = regexp.MustCompile(`^data:image/svg\+xml;(?:charset=[A-Za-z0-9-]+,[A-Za-z0-9%._~:/?#\[\]@!$&()*+,;=-]+|base64,[A-Za-z0-9+/=]+)$`)

var svgAllowedTags = map[string]bool{
	"svg":            true,
	"g":              true,
	"path":           true,
	"circle":         true,
	"rect":           true,
	"polygon":        true,
	"polyline":       true,
	"line":           true,
	"ellipse":        true,
	"defs":           true,
	"lineargradient": true,
	"radialgradient": true,
	"stop":           true,
	"title":          true,
	"desc":           true,
}

var svgAllowedAttrs = map[string]bool{
	"xmlns":             true,
	"viewbox":           true,
	"width":             true,
	"height":            true,
	"x":                 true,
	"y":                 true,
	"x1":                true,
	"y1":                true,
	"x2":                true,
	"y2":                true,
	"cx":                true,
	"cy":                true,
	"r":                 true,
	"rx":                true,
	"ry":                true,
	"d":                 true,
	"points":            true,
	"fill":              true,
	"fill-opacity":      true,
	"fill-rule":         true,
	"stroke":            true,
	"stroke-width":      true,
	"stroke-linecap":    true,
	"stroke-linejoin":   true,
	"stroke-opacity":    true,
	"opacity":           true,
	"offset":            true,
	"stop-color":        true,
	"stop-opacity":      true,
	"gradientunits":     true,
	"gradienttransform": true,
	"transform":         true,
	"id":                true,
	"class":             true,
}

func isDangerousSVGContent(svg string) bool {
	lower := strings.ToLower(svg)
	if strings.Contains(lower, "&#") || strings.Contains(lower, "<!") || strings.Contains(lower, "<?") {
		return true
	}
	dec := xml.NewDecoder(strings.NewReader(svg))
	dec.Strict = true
	hasRootSVG := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return true
		}
		switch t := tok.(type) {
		case xml.ProcInst, xml.Directive:
			return true
		case xml.StartElement:
			tag := strings.ToLower(t.Name.Local)
			if !svgAllowedTags[tag] {
				return true
			}
			if tag == "svg" {
				hasRootSVG = true
			}
			for _, attr := range t.Attr {
				attrName := strings.ToLower(attr.Name.Local)
				if strings.HasPrefix(attrName, "on") || !svgAllowedAttrs[attrName] {
					return true
				}
				valLower := strings.ToLower(attr.Value)
				if strings.Contains(valLower, "javascript:") ||
					strings.Contains(valLower, "vbscript:") ||
					strings.Contains(valLower, "data:") ||
					strings.Contains(valLower, "url(") {
					return true
				}
			}
		}
	}
	return !hasRootSVG
}

func isValidImageDataURI(raw string) bool {
	if len(raw) == 0 || len(raw) > 3*1024*1024 {
		return false
	}
	if validImageDataURIRe.MatchString(raw) {
		return true
	}
	if validSvgDataURIRe.MatchString(raw) {
		if idx := strings.Index(strings.ToLower(raw), ";base64,"); idx != -1 {
			b64Payload := raw[idx+len(";base64,"):]
			decoded, err := base64.StdEncoding.DecodeString(b64Payload)
			if err != nil {
				return false
			}
			return !isDangerousSVGContent(string(decoded))
		}
		commaIdx := strings.IndexByte(raw, ',')
		if commaIdx == -1 {
			return false
		}
		decoded := raw[commaIdx+1:]
		for pass := 0; pass < 2; pass++ {
			unescaped, err := url.PathUnescape(decoded)
			if err != nil {
				return false
			}
			if unescaped == decoded {
				break
			}
			decoded = unescaped
		}
		return !isDangerousSVGContent(decoded)
	}
	return false
}

func extractInstanceMetadata(instFolder string, instName string) map[string]any {
	meta := map[string]any{
		"realName": instName,
		"loader":   "vanilla",
		"iconData": nil,
	}

	var iconField string
	var loaderField string
	var nameField string

	instJsonPath := filepath.Join(instFolder, "instance.json")
	var data []byte
	var err error
	if data, err = os.ReadFile(instJsonPath); err != nil {
		configPath := filepath.Join(instFolder, "gens", "launcher_config.json")
		data, err = os.ReadFile(configPath)
	}

	if err == nil {
		var instData struct {
			Name     string `json:"name"`
			Loader   string `json:"loader"`
			Icon     string `json:"icon"`
			IconData string `json:"iconData"`
		}
		if err := json.Unmarshal(data, &instData); err == nil {
			nameField = instData.Name
			loaderField = instData.Loader
			if instData.Icon != "" {
				iconField = instData.Icon
			} else if instData.IconData != "" {
				iconField = instData.IconData
			}
		}
	}

	if nameField != "" {
		meta["realName"] = nameField
	}
	if loaderField != "" {
		meta["loader"] = loaderField
	}

	var finalIconData string

	if isValidImageDataURI(iconField) {
		finalIconData = iconField
	} else if strings.HasPrefix(iconField, "file://") {
		filePath := strings.TrimPrefix(iconField, "file://")
		if strings.HasPrefix(filePath, "/") && len(filePath) > 2 && filePath[2] == ':' {
			filePath = filePath[1:]
		}
		filePath = filepath.FromSlash(filePath)
		if isRealPathStrictlyUnder(instFolder, filePath) {
			if info, err := os.Stat(filePath); err == nil && !info.IsDir() && info.Size() < 2*1024*1024 {
				if b64, mime, err := readFileBase64(filePath); err == nil {
					finalIconData = fmt.Sprintf("data:%s;base64,%s", mime, b64)
				}
			}
		}
	}

	if finalIconData == "" {
		exts := []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"}
		for _, ext := range exts {
			candidate := filepath.Join(instFolder, "icon"+ext)
			if !isRealPathStrictlyUnder(instFolder, candidate) {
				continue
			}
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Size() < 2*1024*1024 {
				if b64, mime, err := readFileBase64(candidate); err == nil {
					finalIconData = fmt.Sprintf("data:%s;base64,%s", mime, b64)
					break
				}
			}
		}
	}

	if finalIconData != "" {
		meta["iconData"] = finalIconData
	}

	return meta
}
