// Package config resolves where the CLI talks to and where it keeps state.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const DefaultAPIURL = "https://api.supercool.sh"

// Settings are the global flags and environment, resolved once per run.
type Settings struct {
	APIURL  string // --api-url, SUPERCOOL_API_URL
	Profile string // --profile, SUPERCOOL_PROFILE
	Token   string // --token, SUPERCOOL_TOKEN (a personal access token)
	JSON    bool
	Quiet   bool
	NoColor bool
}

// Resolve fills in defaults from the environment.
func (s *Settings) Resolve() {
	if s.APIURL == "" {
		s.APIURL = os.Getenv("SUPERCOOL_API_URL")
	}
	if s.APIURL == "" {
		s.APIURL = DefaultAPIURL
	}
	s.APIURL = strings.TrimRight(s.APIURL, "/")
	if s.Profile == "" {
		s.Profile = os.Getenv("SUPERCOOL_PROFILE")
	}
	if s.Profile == "" {
		s.Profile = "default"
	}
	if s.Token == "" {
		s.Token = strings.TrimSpace(os.Getenv("SUPERCOOL_TOKEN"))
	}
	if os.Getenv("NO_COLOR") != "" {
		s.NoColor = true
	}
}

// Dir is the CLI's config directory (~/.config/supercool, or the OS equivalent).
func Dir() (string, error) {
	if d := os.Getenv("SUPERCOOL_CONFIG_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o700)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	d := filepath.Join(base, "supercool")
	return d, os.MkdirAll(d, 0o700)
}

// Hash is a short stable hash for directory names.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
