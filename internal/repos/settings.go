package repos

import "jidoka/internal/db"

// kvSettings holds the settings document (TOML).
const kvSettings = "settings"

// GetSettings returns the settings document; false when none was saved.
func GetSettings(q db.Queryer) (string, bool, error) {
	return GetKV(q, kvSettings)
}

// PutSettings stores the settings document.
func PutSettings(q db.Queryer, doc string) error {
	return SetKV(q, kvSettings, doc)
}
