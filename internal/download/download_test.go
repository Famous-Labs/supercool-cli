package download

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Famous-Labs/supercool-cli/internal/api"
	"github.com/Famous-Labs/supercool-cli/internal/journal"
)

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "passwd",
		"..\\..\\boot.ini": "boot.ini",
		".hidden":          "hidden",
		"CON.txt":          "_CON.txt",
		"a\x00b\nc.png":    "a_b_c.png",
		"what?.mp4":        "what_.mp4",
		"..":               "file",
		"":                 "file",
		"trailing. ":       "trailing",
		"normal name.mov":  "normal name.mov",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlug(t *testing.T) {
	if got := Slug("Candle Shop Ad!", "s1"); got != "candle-shop-ad" {
		t.Fatal(got)
	}
	if got := Slug("", "0123456789abcdef"); got != "work-01234567" {
		t.Fatal(got)
	}
	if got := Slug("../../x", "s"); strings.Contains(got, "/") || strings.Contains(got, ".") {
		t.Fatal(got)
	}
}

func TestInsideRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	if _, err := inside(dir, "../x"); err == nil {
		t.Fatal("escaped")
	}
	if _, err := inside(dir, "sub/x"); err == nil {
		t.Fatal("nested path allowed")
	}
}

// fileServer serves one file by revision, like the signed-link route: 412
// when the pinned revision no longer matches, Range supported.
type fileServer struct {
	mu      sync.Mutex
	content []byte
	rev     string
	gets    []string
	cutAt   int // close the connection after this many bytes (once)
}

func (s *fileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets = append(s.gets, r.URL.Path+" "+r.Header.Get("Range"))
	if strings.HasPrefix(r.URL.Path, "/api/v1/agent/files/") {
		fmt.Fprintf(w, `{"id":"s1/a.bin","work_id":"s1","file_name":"a.bin","kind":"doc","url":"%s/dl/%s","revision":%q,"size":%d}`,
			"http://"+r.Host, s.rev, s.rev, len(s.content))
		return
	}
	pinned := strings.TrimPrefix(r.URL.Path, "/dl/")
	if pinned != s.rev {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	data := s.content
	start := 0
	if rg := r.Header.Get("Range"); rg != "" {
		start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
	}
	body := data[start:]
	if s.cutAt > 0 && len(body) > s.cutAt {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if start == 0 {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write(body[:s.cutAt])
		s.cutAt = 0
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
		return
	}
	_, _ = w.Write(body)
}

func newSaver(t *testing.T, srv *httptest.Server) (*Saver, *journal.Journal) {
	t.Setenv("SUPERCOOL_CONFIG_DIR", t.TempDir())
	j, err := journal.Open(srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	return &Saver{Client: api.New(srv.URL, nil), Journal: j}, j
}

func file(srv *httptest.Server, rev string, size int) api.File {
	r := rev
	n := int64(size)
	return api.File{ID: "s1/a.bin", WorkID: "s1", FileName: "a.bin", Kind: "doc", URL: srv.URL + "/dl/" + rev, Revision: &r, Size: &n}
}

func TestResumeAfterAnInterruptedDownload(t *testing.T) {
	fs := &fileServer{content: []byte(strings.Repeat("abcdefghij", 1000)), rev: "r1", cutAt: 3000}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, _ := newSaver(t, srv)
	dir := t.TempDir()
	if _, err := s.Save(context.Background(), file(srv, "r1", 10000), dir); err == nil {
		t.Fatal("expected an interrupted download")
	}
	p, err := s.Save(context.Background(), file(srv, "r1", 10000), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(fs.content) {
		t.Fatalf("resumed file differs (%d bytes)", len(got))
	}
	if !strings.Contains(strings.Join(fs.gets, "|"), "bytes=3000-") {
		t.Fatalf("did not resume with a Range: %v", fs.gets)
	}
}

func TestAChangedFileGetsAFreshLinkAndNeverMixesRevisions(t *testing.T) {
	fs := &fileServer{content: []byte("NEW CONTENT!"), rev: "r2"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, _ := newSaver(t, srv)
	dir := t.TempDir()
	// A partial of the OLD revision is on disk.
	old := file(srv, "r1", 12)
	if err := os.WriteFile(filepath.Join(dir, partialName(old)), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	var recorded []string
	s.OnRevision = func(o, f api.File) { recorded = append(recorded, o.Rev()+"->"+f.Rev()) }
	p, err := s.Save(context.Background(), old, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "NEW CONTENT!" {
		t.Fatalf("got %q", got)
	}
	if len(recorded) != 1 || recorded[0] != "r1->r2" {
		t.Fatalf("revision change not recorded before the restart: %v", recorded)
	}
	if _, err := os.Stat(filepath.Join(dir, partialName(old))); !os.IsNotExist(err) {
		t.Fatal("the old revision's partial was left behind")
	}
}

func TestTheLedgerSkipsWhatIsSavedAndNeverMakesCopies(t *testing.T) {
	fs := &fileServer{content: []byte("hello"), rev: "r1"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, _ := newSaver(t, srv)
	dir := t.TempDir()
	a, _ := s.Save(context.Background(), file(srv, "r1", 5), dir)
	b, _ := s.Save(context.Background(), file(srv, "r1", 5), dir)
	if a != b {
		t.Fatalf("a rerun made a copy: %s vs %s", a, b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("want 1 file, got %d", len(entries))
	}
	// A new revision of the same file is downloaded fresh, next to it.
	fs.content, fs.rev = []byte("hello v2"), "r2"
	c, err := s.Save(context.Background(), file(srv, "r2", 8), dir)
	if err != nil || c == a {
		t.Fatalf("new revision not saved separately: %s %v", c, err)
	}
}

func TestNothingIsWrittenThroughASymlink(t *testing.T) {
	fs := &fileServer{content: []byte("x"), rev: "r1"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, _ := newSaver(t, srv)
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "victim")
	f := file(srv, "r1", 1)
	if err := os.Symlink(target, filepath.Join(dir, partialName(f))); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := s.Save(context.Background(), f, dir); err == nil {
		t.Fatal("wrote through a symlink")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the symlink target was created")
	}
}

func TestAFreshWriteNeverTruncatesASymlinkTarget(t *testing.T) {
	fs := &fileServer{content: []byte("new"), rev: "r1"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, _ := newSaver(t, srv)
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "important.txt")
	if err := os.WriteFile(victim, []byte("KEEP ME"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := file(srv, "r1", 3)
	// The link appears after the pre-check (a race): simulate by planting it
	// and calling saveFile directly past Save's checks.
	if err := os.Symlink(victim, filepath.Join(dir, partialName(f))); err != nil {
		t.Skip("symlinks unavailable")
	}
	_, _ = s.saveFile(context.Background(), f, dir)
	if got, _ := os.ReadFile(victim); string(got) != "KEEP ME" {
		t.Fatalf("the symlink target was modified: %q", got)
	}
}
