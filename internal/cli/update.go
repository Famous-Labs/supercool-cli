package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Famous-Labs/supercool-cli/internal/config"
	"github.com/Famous-Labs/supercool-cli/internal/version"
)

const releasesAPI = "https://api.github.com/repos/Famous-Labs/supercool-cli/releases/latest"

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func latestRelease(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("couldn't check for updates: %s", resp.Status)
	}
	var r release
	return &r, json.NewDecoder(resp.Body).Decode(&r)
}

// installMethod guesses how this binary was installed.
func installMethod() (string, string) {
	exe, err := os.Executable()
	if err != nil {
		return "unknown", ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	low := strings.ToLower(filepath.ToSlash(exe))
	switch {
	case strings.Contains(low, "/cellar/") || strings.Contains(low, "/homebrew/") || strings.Contains(low, "/linuxbrew/"):
		return "brew", exe
	case strings.Contains(low, "node_modules"):
		return "npm", exe
	}
	return "binary", exe
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update the CLI to the latest version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			rel, err := latestRelease(ctx)
			if err != nil {
				return err
			}
			latest := strings.TrimPrefix(rel.Tag, "v")
			if latest == version.Version {
				a.ui.Success("Up to date (%s).", version.Version)
				a.ui.Result(map[string]any{"version": version.Version, "latest": latest, "updated": false})
				return nil
			}
			method, exe := installMethod()
			switch method {
			case "brew":
				a.ui.Info("Update with: brew upgrade supercool")
			case "npm":
				a.ui.Info("Update with: npm i -g @famous-labs/supercool-cli@latest")
			default:
				a.ui.Status("Downloading %s…", latest)
				if err := selfUpdate(ctx, rel, latest, exe); err != nil {
					return err
				}
				a.ui.Success("Updated to %s.", latest)
			}
			a.ui.Result(map[string]any{"version": version.Version, "latest": latest, "install_method": method,
				"updated": method == "binary"})
			return nil
		},
	}
}

func fetch(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

// selfUpdate replaces this binary with the release's, after checking the
// archive against the release's checksums.txt.
func selfUpdate(ctx context.Context, rel *release, ver, exe string) error {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	want := fmt.Sprintf("supercool_%s_%s_%s%s", ver, runtime.GOOS, runtime.GOARCH, ext)
	var archiveURL, sumsURL string
	for _, as := range rel.Assets {
		switch as.Name {
		case want:
			archiveURL = as.URL
		case "checksums.txt":
			sumsURL = as.URL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("no %s in release %s", want, rel.Tag)
	}
	sums, err := fetch(ctx, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	archive, err := fetch(ctx, archiveURL, 200<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	ok := false
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == want && f[0] == hex.EncodeToString(sum[:]) {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("checksum mismatch for %s; not updating", want)
	}
	bin, err := binaryFrom(archive, ext)
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return fmt.Errorf("can't write next to %s (try with sudo, or reinstall): %w", exe, err)
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
		if err := os.Rename(exe, exe+".old"); err != nil {
			return err
		}
	}
	return os.Rename(tmp, exe)
}

func binaryFrom(archive []byte, ext string) ([]byte, error) {
	name := "supercool"
	if runtime.GOOS == "windows" {
		name = "supercool.exe"
	}
	if ext == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 200<<20))
			}
		}
		return nil, fmt.Errorf("%s not in the archive", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not in the archive", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// updateNotice returns a one-line notice when a newer version exists,
// checking at most once a day (cached), and never blocking long.
func updateNotice(ctx context.Context) string {
	if version.Version == "dev" || os.Getenv("SUPERCOOL_NO_UPDATE_CHECK") != "" {
		return ""
	}
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "update-check.json")
	var cache struct {
		CheckedAt time.Time `json:"checked_at"`
		Latest    string    `json:"latest"`
	}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	if time.Since(cache.CheckedAt) > 24*time.Hour {
		cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		if rel, err := latestRelease(cctx); err == nil {
			cache.Latest = strings.TrimPrefix(rel.Tag, "v")
		}
		cache.CheckedAt = time.Now()
		if data, err := json.Marshal(cache); err == nil {
			_ = os.WriteFile(p, data, 0o600)
		}
	}
	if cache.Latest != "" && cache.Latest != version.Version {
		return fmt.Sprintf("A new SuperCool CLI is out (%s → %s): supercool update", version.Version, cache.Latest)
	}
	return ""
}
