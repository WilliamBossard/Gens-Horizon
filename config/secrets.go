package config

// Variables injectÃ©es au moment du build via -ldflags
// Exemple : -ldflags="-X 'github.com/WilliamBossard/Gens-Horizon/config.GoogleClientID=xxx' ..."
var (
	GoogleClientID     = ""
	GoogleClientSecret = ""
	DropboxClientID    = ""
	DropboxClientSecret= ""
	OneDriveClientID   = ""
	AppVersion         = "1.4.1"
)

