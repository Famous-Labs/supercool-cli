// Package auth handles login, stored credentials and token refresh.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Famous-Labs/supercool-cli/internal/config"
)

const keyringService = "supercool-cli"

// Credentials are one profile's login for one API.
type Credentials struct {
	APIURL        string    `json:"api_url"`
	ClientID      string    `json:"client_id"`
	TokenEndpoint string    `json:"token_endpoint"`
	RevokeURL     string    `json:"revocation_endpoint"`
	Resource      string    `json:"resource"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	ExpiresAt     time.Time `json:"expires_at"`
	Subject       string    `json:"subject"` // the user id (the token's sub)
}

// Key names one profile's credentials for one API.
func Key(profile, apiURL string) string {
	return profile + "@" + config.Hash(apiURL)
}

func fallbackPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func readFallback() (map[string]Credentials, error) {
	p, err := fallbackPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Credentials{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := map[string]Credentials{}
	if err := json.Unmarshal(data, &all); err != nil {
		return map[string]Credentials{}, nil
	}
	return all, nil
}

func writeFallback(all map[string]Credentials) error {
	p, err := fallbackPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Load returns stored credentials (keychain first, then the file), or nil.
func Load(key string) (*Credentials, error) {
	if s, err := keyring.Get(keyringService, key); err == nil {
		var c Credentials
		if json.Unmarshal([]byte(s), &c) == nil {
			return &c, nil
		}
	}
	all, err := readFallback()
	if err != nil {
		return nil, err
	}
	if c, ok := all[key]; ok {
		return &c, nil
	}
	return nil, nil
}

// Save stores credentials in the OS keychain, or the 0600 file when there
// is no keychain (headless Linux, CI).
func Save(key string, c *Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := keyring.Set(keyringService, key, string(data)); err == nil {
		// Don't leave an older copy behind in the file.
		if all, ferr := readFallback(); ferr == nil {
			if _, ok := all[key]; ok {
				delete(all, key)
				_ = writeFallback(all)
			}
		}
		return nil
	}
	all, err := readFallback()
	if err != nil {
		return err
	}
	all[key] = *c
	return writeFallback(all)
}

// Delete removes stored credentials everywhere.
func Delete(key string) error {
	_ = keyring.Delete(keyringService, key)
	all, err := readFallback()
	if err != nil {
		return err
	}
	if _, ok := all[key]; ok {
		delete(all, key)
		return writeFallback(all)
	}
	return nil
}
