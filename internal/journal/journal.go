// Package journal is the CLI's local record of messages, results and
// downloads, so retries and downloads survive crashes and never repeat.
//
// It lives under <config>/state/<api-hash>/<connection-hash>/: per API URL
// and per authenticated connection (a personal access token gets its own),
// so switching accounts or environments never mixes state.
//
//	requests/<request_id>.json  a message: written BEFORE it is sent (a lost
//	                            response retries with the same id, which the
//	                            server deduplicates), then its cursor, work,
//	                            outcomes and pending files
//	downloads.json              every saved file by file id + revision
//
// Writes are atomic (temp file + rename) under a per-directory file lock.
package journal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/Famous-Labs/supercool-cli/internal/api"
	"github.com/Famous-Labs/supercool-cli/internal/config"
)

const keepFinished = 7 * 24 * time.Hour

// Request is one message's local record.
type Request struct {
	ID         string            `json:"id"`
	Message    string            `json:"message_preview"`
	CreatedAt  time.Time         `json:"created_at"`
	Sent       bool              `json:"sent"`
	Status     string            `json:"status"` // the turn's status
	Cursor     string            `json:"cursor"` // scoped updates cursor, advanced only after entries are saved
	Out        string            `json:"out,omitempty"`
	Work       map[string]string `json:"work"`     // work id -> title
	Outcomes   map[string]string `json:"outcomes"` // work id -> outcome
	Pending    []api.File        `json:"pending"`  // files received but not saved yet
	Reply      string            `json:"reply,omitempty"`
	Finished   bool              `json:"finished"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	UploadIDs  []string          `json:"upload_ids,omitempty"`
	// What was sent, to resend it with the same id if the agent was busy.
	Text      string   `json:"text,omitempty"`
	FilePaths []string `json:"file_paths,omitempty"`
	Recovered int      `json:"recovered,omitempty"`
	// Each piece of work's finished result text (for --json and reruns).
	Results  map[string]string `json:"results,omitempty"`
	Streamed bool              `json:"streamed,omitempty"` // --out -: the one file already went to stdout
}

// Download is one saved file.
type Download struct {
	FileID   string    `json:"file_id"`
	Revision string    `json:"revision"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	SavedAt  time.Time `json:"saved_at"`
}

// Journal is one connection's state directory.
type Journal struct {
	dir  string
	lock *flock.Flock
}

// Open returns the journal for (apiURL, connection identity).
func Open(apiURL, identity string) (*Journal, error) {
	base, err := config.Dir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "state", config.Hash(apiURL), config.Hash(identity))
	if err := os.MkdirAll(filepath.Join(dir, "requests"), 0o700); err != nil {
		return nil, err
	}
	return &Journal{dir: dir, lock: flock.New(filepath.Join(dir, ".lock"))}, nil
}

// Dir is the journal's directory.
func (j *Journal) Dir() string { return j.dir }

func (j *Journal) locked(fn func() error) error {
	if err := j.lock.Lock(); err != nil {
		return err
	}
	defer j.lock.Unlock()
	return fn()
}

func writeAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (j *Journal) reqPath(id string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' {
			return '_'
		}
		return r
	}, id)
	return filepath.Join(j.dir, "requests", safe+".json")
}

// SaveRequest writes a request record.
func (j *Journal) SaveRequest(r *Request) error {
	return j.locked(func() error { return writeAtomic(j.reqPath(r.ID), r) })
}

// LoadRequest reads a request record (nil when there's none).
func (j *Journal) LoadRequest(id string) (*Request, error) {
	data, err := os.ReadFile(j.reqPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Request
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Work == nil {
		r.Work = map[string]string{}
	}
	if r.Outcomes == nil {
		r.Outcomes = map[string]string{}
	}
	if r.Results == nil {
		r.Results = map[string]string{}
	}
	return &r, nil
}

// All returns every request, newest first.
func (j *Journal) All() ([]*Request, error) {
	entries, err := os.ReadDir(filepath.Join(j.dir, "requests"))
	if err != nil {
		return nil, err
	}
	var all []*Request
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if r, err := j.LoadRequest(strings.TrimSuffix(e.Name(), ".json")); err == nil && r != nil {
			all = append(all, r)
		}
	}
	sort.Slice(all, func(a, b int) bool { return all[a].CreatedAt.After(all[b].CreatedAt) })
	return all, nil
}

// Unfinished returns the requests still waiting on work or downloads,
// oldest first (for `wait` with no id).
func (j *Journal) Unfinished() ([]*Request, error) {
	all, err := j.All()
	if err != nil {
		return nil, err
	}
	var out []*Request
	for i := len(all) - 1; i >= 0; i-- {
		if !all[i].Finished {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// Prune drops finished requests older than a week.
func (j *Journal) Prune() {
	entries, err := os.ReadDir(filepath.Join(j.dir, "requests"))
	if err != nil {
		return
	}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if r, err := j.LoadRequest(id); err == nil && r != nil && r.Finished && r.FinishedAt != nil &&
			time.Since(*r.FinishedAt) > keepFinished {
			_ = os.Remove(j.reqPath(id))
		}
	}
}

func (j *Journal) downloadsPath() string { return filepath.Join(j.dir, "downloads.json") }

func (j *Journal) readDownloads() map[string]Download {
	all := map[string]Download{}
	if data, err := os.ReadFile(j.downloadsPath()); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	return all
}

func dlKey(fileID, revision string) string { return fileID + "\x00" + revision }

// Saved returns the saved copy of this exact revision, if it is still on disk
// with the recorded size.
func (j *Journal) Saved(fileID, revision string) (*Download, bool) {
	d, ok := j.readDownloads()[dlKey(fileID, revision)]
	if !ok {
		return nil, false
	}
	st, err := os.Stat(d.Path)
	if err != nil || (d.Size > 0 && st.Size() != d.Size) {
		return nil, false
	}
	return &d, true
}

// RecordSaved notes a completed download.
func (j *Journal) RecordSaved(d Download) error {
	return j.locked(func() error {
		all := j.readDownloads()
		all[dlKey(d.FileID, d.Revision)] = d
		return writeAtomic(j.downloadsPath(), all)
	})
}
