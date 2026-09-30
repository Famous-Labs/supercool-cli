// Package download saves deliverables to disk safely and exactly once.
//
//   - Remote names and titles are sanitized (no separators, "..", control
//     characters, leading dots or Windows reserved names) and every path is
//     checked to stay inside the output folder; nothing is written through
//     a symlink and nothing is overwritten.
//   - Each link is pinned to one revision of the file. Partial downloads are
//     named per revision (<name>.<rev-hash>.part), so a partial can only be
//     resumed by the revision it started with. If the file changed, the
//     server answers 412: fetch fresh metadata and a new pinned link, record
//     the new revision, start a new partial. Bytes from two revisions never mix.
//   - The journal's download ledger (file id + revision) makes reruns skip
//     what is already saved instead of making -2, -3 copies.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Famous-Labs/supercool-cli/internal/api"
	"github.com/Famous-Labs/supercool-cli/internal/config"
	"github.com/Famous-Labs/supercool-cli/internal/journal"
	"github.com/Famous-Labs/supercool-cli/internal/version"
)

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func init() {
	for i := 1; i <= 9; i++ {
		reserved["COM"+strconv.Itoa(i)] = true
		reserved["LPT"+strconv.Itoa(i)] = true
	}
}

// SafeName turns a remote file name into one safe to create in a folder.
func SafeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r), strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	out = strings.TrimRight(out, ". ")
	if out == "" || out == ".." {
		out = "file"
	}
	stem := strings.ToUpper(strings.SplitN(out, ".", 2)[0])
	if reserved[stem] {
		out = "_" + out
	}
	if len(out) > 150 {
		ext := filepath.Ext(out)
		if len(ext) > 20 {
			ext = ""
		}
		out = out[:150-len(ext)] + ext
	}
	return out
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug is a folder name for a piece of work.
func Slug(title, workID string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		id := nonSlug.ReplaceAllString(strings.ToLower(workID), "")
		if len(id) > 8 {
			id = id[:8]
		}
		s = "work-" + id
	}
	return s
}

// EnsureDir creates dir (and parents) and refuses a symlinked final folder we
// would write into.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		// The user may point --out at a symlink themselves; resolve it once
		// and require a real directory there.
		real, rerr := filepath.EvalSymlinks(dir)
		if rerr != nil {
			return rerr
		}
		if rst, serr := os.Stat(real); serr != nil || !rst.IsDir() {
			return fmt.Errorf("%s is not a folder", dir)
		}
	} else if !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	return nil
}

// inside joins dir and a sanitized name and checks the result stays in dir.
func inside(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	if filepath.Dir(p) != filepath.Clean(dir) {
		return "", fmt.Errorf("refusing to write outside %s", dir)
	}
	return p, nil
}

func isSymlink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// freeName picks name, then name-2.ext, name-3.ext … that doesn't exist yet.
func freeName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		cand := name
		if i > 1 {
			cand = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		p, err := inside(dir, cand)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
	}
	return "", fmt.Errorf("too many copies of %s in %s", name, dir)
}

// ErrChanged means the file changed while downloading and no fresh link could
// be had.
var ErrChanged = errors.New("the file changed on the server")

// Saver downloads files for one journal.
type Saver struct {
	Client  *api.Client
	Journal *journal.Journal
	HTTP    *http.Client
	// OnRevision is called when a file's revision changed (412) and a fresh
	// record was fetched, BEFORE the new download starts, so the caller can
	// record the new revision in its request journal.
	OnRevision func(old, fresh api.File)
}

func (s *Saver) http() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 0}
}

// Save downloads f into dir and returns the saved path (the existing one when
// this revision is already on disk).
func (s *Saver) Save(ctx context.Context, f api.File, dir string) (string, error) {
	if err := EnsureDir(dir); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 3; attempt++ {
		if d, ok := s.Journal.Saved(f.ID, f.Rev()); ok {
			return d.Path, nil
		}
		var path string
		var err error
		if f.Kind == "site" {
			path, err = s.saveSite(ctx, f, dir)
		} else {
			path, err = s.saveFile(ctx, f, dir)
		}
		if errors.Is(err, ErrChanged) && f.ID != "" {
			fresh, ferr := s.Client.FileRecord(ctx, f.ID)
			if ferr != nil {
				return "", fmt.Errorf("%s changed on the server and couldn't be re-fetched: %w", f.FileName, ferr)
			}
			if s.OnRevision != nil {
				s.OnRevision(f, *fresh)
			}
			s.dropPartial(f, dir)
			f = *fresh
			continue
		}
		return path, err
	}
	return "", fmt.Errorf("%s keeps changing on the server; try again later", f.FileName)
}

func partialName(f api.File) string {
	return SafeName(f.FileName) + "." + config.Hash(f.Rev())[:8] + ".part"
}

func (s *Saver) dropPartial(f api.File, dir string) {
	if p, err := inside(dir, partialName(f)); err == nil && !isSymlink(p) {
		_ = os.Remove(p)
	}
}

func (s *Saver) saveFile(ctx context.Context, f api.File, dir string) (string, error) {
	if f.URL == "" {
		return "", fmt.Errorf("%s has no download link", f.FileName)
	}
	part, err := inside(dir, partialName(f))
	if err != nil {
		return "", err
	}
	if isSymlink(part) {
		return "", fmt.Errorf("refusing to write through a symlink: %s", part)
	}
	var offset int64
	if st, err := os.Lstat(part); err == nil && st.Mode().IsRegular() {
		offset = st.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := s.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return "", ErrChanged
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// The partial is as long as the file (or longer): start over.
		_ = os.Remove(part)
		return "", ErrChanged
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		if !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset)) {
			return "", fmt.Errorf("the server resumed %s at the wrong place", f.FileName)
		}
	case resp.StatusCode == http.StatusOK:
		offset = 0 // no resume: write from the start
	default:
		return "", fmt.Errorf("download of %s failed: %s", f.FileName, resp.Status)
	}
	// Opening never follows a symlink, atomically: a fresh write removes
	// whatever is at the partial's name (a link itself, never its target)
	// and creates it exclusively, so anything that appears in between makes
	// the open fail; an append opens without following a link (O_NOFOLLOW,
	// or the reparse point itself on Windows). Nothing is written through a link.
	var out *os.File
	if offset > 0 {
		out, err = openAppend(part)
	} else {
		if rerr := os.Remove(part); rerr != nil && !os.IsNotExist(rerr) {
			return "", rerr
		}
		out, err = os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o644)
	}
	if err != nil {
		return "", fmt.Errorf("can't write %s safely: %w", part, err)
	}
	n, cerr := io.Copy(out, resp.Body)
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return "", fmt.Errorf("download of %s interrupted after %d bytes (it resumes next time): %w", f.FileName, offset+n, cerr)
	}
	total := offset + n
	if f.Size != nil && *f.Size > 0 && total != *f.Size {
		return "", fmt.Errorf("download of %s is incomplete (%d of %d bytes; it resumes next time)", f.FileName, total, *f.Size)
	}
	final, err := freeName(dir, SafeName(f.FileName))
	if err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	_ = s.Journal.RecordSaved(journal.Download{FileID: f.ID, Revision: f.Rev(), Path: final, Size: total, SavedAt: time.Now()})
	return final, nil
}

// saveSite writes a .url shortcut to the live site (a site has no file).
func (s *Saver) saveSite(ctx context.Context, f api.File, dir string) (string, error) {
	target := f.URL
	if f.URL != "" {
		noFollow := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
		if err == nil {
			req.Header.Set("User-Agent", version.UserAgent())
			if resp, err := noFollow.Do(req); err == nil {
				if loc := resp.Header.Get("Location"); loc != "" {
					target = loc
				}
				resp.Body.Close()
			}
		}
	}
	name := strings.TrimSuffix(SafeName(f.FileName), filepath.Ext(f.FileName)) + ".url"
	if f.Label != "" {
		name = SafeName(f.Label) + ".url"
	}
	p, err := freeName(dir, name)
	if err != nil {
		return "", err
	}
	body := "[InternetShortcut]\r\nURL=" + target + "\r\n"
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, werr := fh.WriteString(body)
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	_ = s.Journal.RecordSaved(journal.Download{FileID: f.ID, Revision: f.Rev(), Path: p, Size: int64(len(body)), SavedAt: time.Now()})
	return p, nil
}

// Stream writes one file to w (for `--out -`).
func Stream(ctx context.Context, f api.File, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download of %s failed: %s", f.FileName, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
