package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func tarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractSkillsInstallsOnlySkillFoldersSafely(t *testing.T) {
	data := tarball(t, map[string]string{
		"supercool-skills-main/README.md":                       "readme",
		"supercool-skills-main/skills/supercool/SKILL.md":       "---\nname: supercool\n---",
		"supercool-skills-main/skills/supercool/ref/notes.md":   "notes",
		"supercool-skills-main/skills/supercool-video/SKILL.md": "---\nname: supercool-video\n---",
		"supercool-skills-main/skills/not-a-skill/other.md":     "no SKILL.md here",
		"supercool-skills-main/skills/evil/../../../escape.md":  "nope",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer srv.Close()
	old := skillsTarball
	skillsTarball = srv.URL
	defer func() { skillsTarball = old }()
	dest := t.TempDir()
	names, err := extractSkills(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "supercool" || names[1] != "supercool-video" {
		t.Fatalf("installed %v", names)
	}
	if _, err := os.Stat(filepath.Join(dest, "supercool", "ref", "notes.md")); err != nil {
		t.Fatal("supporting file missing")
	}
	if _, err := os.Stat(filepath.Join(dest, "not-a-skill")); !os.IsNotExist(err) {
		t.Fatal("a folder without SKILL.md was installed")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.md")); !os.IsNotExist(err) {
		t.Fatal("a path escaped the skills folder")
	}
}
