package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Famous-Labs/supercool-cli/internal/api"
	"github.com/Famous-Labs/supercool-cli/internal/download"
	"github.com/Famous-Labs/supercool-cli/internal/exitcode"
	"github.com/Famous-Labs/supercool-cli/internal/journal"
)

const (
	maxMessageChars = 20000
	inlineFileMax   = 8 << 20  // files up to this go inline; bigger ones are uploaded
	inlineTotalMax  = 20 << 20 // inline total per message
	pollWait        = 30
	busyRetries     = 5
)

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "cli_" + hex.EncodeToString(b)
}

func preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		return s[:77] + "…"
	}
	return s
}

func askCmd() *cobra.Command {
	var (
		files     []string
		wait      bool
		out       string
		timeout   time.Duration
		requestID string
	)
	cmd := &cobra.Command{
		Use:   "ask [message]",
		Short: "Send your agent a message (add --wait to get the finished files)",
		Long: `Send your agent a message, exactly as you would text it.

Without --wait: prints the agent's reply, the work it started and a request id.
With --wait: follows only the work this message started until it's finished,
saves every file into --out (default ./supercool/<work-title>/) and prints the paths.

  supercool ask "a 15s vertical ad for my candle shop" --wait
  supercool ask "turn this into a trailer" --file ./clip.mov --wait
  cat brief.md | supercool ask --wait
  supercool ask "stop that"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			if err := a.requireLogin(); err != nil {
				return err
			}
			text := strings.TrimSpace(strings.Join(args, " "))
			if text == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
				data, rerr := io.ReadAll(io.LimitReader(os.Stdin, maxMessageChars*4+1))
				if rerr != nil {
					return rerr
				}
				text = strings.TrimSpace(string(data))
			}
			if text == "" && len(files) == 0 {
				return exitcode.New(exitcode.Error, "say what you want: supercool ask \"…\"")
			}
			if len([]rune(text)) > maxMessageChars {
				return exitcode.New(exitcode.Error, fmt.Sprintf("that message is over %d characters; send the long part as a --file", maxMessageChars))
			}
			j, err := a.journal()
			if err != nil {
				return err
			}
			j.Prune()
			if requestID == "" {
				requestID = newRequestID()
			}
			req, err := j.LoadRequest(requestID)
			if err != nil {
				return err
			}
			if req == nil {
				req = &journal.Request{ID: requestID, Message: preview(text), CreatedAt: time.Now(), Text: text,
					FilePaths: files, Out: out, Work: map[string]string{}, Outcomes: map[string]string{},
					Results: map[string]string{}}
			}
			// The effective output is the flag, or what a retried request was
			// started with: check it only now, after the journal is loaded.
			if out != "" {
				req.Out = out
			}
			if req.Out == "-" {
				if a.s.JSON {
					return exitcode.New(exitcode.Error, "--out - streams the file itself to stdout, so it can't be combined with --json")
				}
				a.ui.Out = os.Stderr // stdout carries only the file's bytes
			}
			ctx := cmd.Context()
			if len(req.UploadIDs) == 0 && len(files) > 0 {
				if req.UploadIDs, err = uploadLarge(ctx, a, files); err != nil {
					return err
				}
			}
			// Written BEFORE sending: a lost response retries with this id.
			if err := j.SaveRequest(req); err != nil {
				return err
			}
			turn, err := send(ctx, a, j, req, wait)
			if err != nil {
				return err
			}
			code := report(a, req, turn)
			if !wait {
				if len(turn.Work) > 0 || turn.Status == "processing" {
					a.ui.Info("Follow it with: supercool wait %s", req.ID)
				}
				for _, f := range turn.Files {
					a.ui.Print("%s: %s", f.FileName, f.URL)
				}
				a.ui.Result(askJSON(req, turn, nil, code))
				return silent(code)
			}
			if turn.Status == "busy" || turn.Status == "failed" {
				a.ui.Result(askJSON(req, turn, nil, code))
				return silent(code)
			}
			return follow(ctx, a, j, req, turn, timeout)
		},
	}
	cmd.Flags().StringArrayVarP(&files, "file", "f", nil, "attach a file (repeatable; up to 500 MB each)")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the work to finish and save its files")
	cmd.Flags().StringVarP(&out, "out", "o", "", "where to save files (default ./supercool/<work-title>/; - for stdout)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long (the work keeps going)")
	cmd.Flags().StringVar(&requestID, "request-id", "", "reuse an id to retry the same message safely")
	return cmd
}

// uploadLarge uploads files too big to send inline; returns their upload ids.
func uploadLarge(ctx context.Context, a *app, paths []string) ([]string, error) {
	var ids []string
	var inline int64
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("can't read %s: %w", p, err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%s is a folder; attach files", p)
		}
		if st.Size() <= inlineFileMax && inline+st.Size() <= inlineTotalMax {
			inline += st.Size()
			continue
		}
		ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		a.ui.Status("Uploading %s (%s)…", filepath.Base(p), humanBytes(st.Size()))
		up, err := a.client.CreateUpload(ctx, filepath.Base(p), ctype, st.Size())
		if err != nil {
			return nil, err
		}
		if err := api.PostFile(ctx, up, p, ctype); err != nil {
			return nil, err
		}
		if _, err := a.client.CompleteUpload(ctx, up.UploadID); err != nil {
			return nil, err
		}
		ids = append(ids, up.UploadID)
		a.ui.Success("Uploaded %s", filepath.Base(p))
	}
	return ids, nil
}

// inlineFiles reads the small attachments (the large ones went as uploads).
func inlineFiles(paths []string) ([]api.FileIn, error) {
	var out []api.FileIn
	var total int64
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.Size() > inlineFileMax || total+st.Size() > inlineTotalMax {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		total += st.Size()
		out = append(out, api.FileIn{Name: filepath.Base(p), Base64: base64.StdEncoding.EncodeToString(data),
			ContentType: mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))})
	}
	return out, nil
}

func isNetErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF)
}

// send posts the message, retrying lost responses and (with --wait) a busy
// agent, always with the same request id.
func send(ctx context.Context, a *app, j *journal.Journal, req *journal.Request, retryBusy bool) (*api.Turn, error) {
	inline, err := inlineFiles(req.FilePaths)
	if err != nil {
		return nil, err
	}
	in := api.MessageIn{Message: req.Text, RequestID: req.ID, Files: inline, UploadIDs: req.UploadIDs}
	busy := 0
	for attempt := 0; ; attempt++ {
		a.ui.Status("Sending to your agent…")
		turn, err := a.client.SendMessage(ctx, in)
		if err != nil {
			if isNetErr(err) && attempt < 3 {
				time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
				continue
			}
			return nil, err
		}
		req.Sent, req.Status = true, turn.Status
		if turn.Cursor != "" && req.Cursor == "" {
			req.Cursor = turn.Cursor
		}
		for _, w := range turn.Work {
			req.Work[w.WorkID] = w.Title
		}
		if turn.Reply != nil {
			req.Reply = *turn.Reply
		}
		req.Pending = appendFiles(req.Pending, turn.Files)
		if err := j.SaveRequest(req); err != nil {
			return nil, err
		}
		if turn.Status == "busy" && retryBusy && busy < busyRetries {
			busy++
			wait := turn.RetryAfterSeconds
			if wait <= 0 {
				wait = 15
			}
			a.ui.Status("Your agent is busy with another message; retrying in %ds…", wait)
			select {
			case <-time.After(time.Duration(wait) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		return turn, nil
	}
}

func appendFiles(pending []api.File, add []api.File) []api.File {
	seen := map[string]bool{}
	for _, f := range pending {
		seen[f.ID+"\x00"+f.Rev()] = true
	}
	for _, f := range add {
		if k := f.ID + "\x00" + f.Rev(); !seen[k] {
			pending = append(pending, f)
			seen[k] = true
		}
	}
	return pending
}

// report prints the immediate answer and returns its exit code.
func report(a *app, req *journal.Request, t *api.Turn) int {
	a.ui.Done()
	if t.Reply != nil && *t.Reply != "" {
		a.ui.Print("%s", *t.Reply)
	}
	for _, w := range t.Work {
		title := w.Title
		if title == "" {
			title = "new chat"
		}
		a.ui.Info("Started: %s  %s", title, w.Link)
	}
	for _, n := range t.Notes {
		a.ui.Warn("%s", n)
	}
	switch t.Status {
	case "busy":
		a.ui.Warn("Your agent is busy with another message. Run the same command again in %ds.", max(t.RetryAfterSeconds, 15))
		return exitcode.RateLimited
	case "failed":
		a.ui.Warn("That message didn't finish (%s).", t.Error)
		return exitcode.Failed
	case "processing":
		a.ui.Status("Your agent is still composing its reply…")
	}
	return exitcode.OK
}

// follow waits for the message's work, saving results to the journal before
// the cursor moves and downloading every file.
func follow(ctx context.Context, a *app, j *journal.Journal, req *journal.Request, turn *api.Turn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	saver := &download.Saver{Client: a.client, Journal: j, OnRevision: func(old, fresh api.File) {
		for i, f := range req.Pending {
			if f.ID == old.ID && f.Rev() == old.Rev() {
				req.Pending[i] = fresh
			}
		}
		_ = j.SaveRequest(req) // the new revision is recorded before its download starts
	}}
	var saved []savedFile
	downloadFailed := false
	resendBusy := 0
	quick := turn != nil && turn.Status == "answered" && len(turn.Work) == 0
	for {
		if !quick {
			poll, err := a.client.Updates(ctx, req.Cursor, req.ID, pollWait)
			if err != nil {
				if isNetErr(err) && time.Now().Before(deadline) {
					a.ui.Status("Connection lost; retrying…")
					time.Sleep(3 * time.Second)
					continue
				}
				return err
			}
			resend := false
			for _, e := range poll.Entries {
				if applyEntry(a, req, e) {
					resend = true
				}
			}
			req.Cursor = poll.Cursor // saved together with the entries above, atomically
			if err := j.SaveRequest(req); err != nil {
				return err
			}
			if resend && resendBusy < busyRetries {
				resendBusy++
				a.ui.Status("Your agent was busy; sending your message again…")
				time.Sleep(15 * time.Second)
				if _, err := send(ctx, a, j, req, true); err != nil {
					return err
				}
				continue
			}
			for _, w := range poll.Work {
				if w.State == "running" {
					a.ui.Status("Working: %s…", titleOf(req, w.WorkID))
				}
			}
			s, failed := savePending(ctx, a, j, req, saver)
			saved = append(saved, s...)
			downloadFailed = downloadFailed || failed
			if poll.Done && !poll.More {
				break
			}
			if poll.More {
				continue
			}
			if time.Now().After(deadline) {
				a.ui.Warn("Still working after %s. It keeps going; rejoin with: supercool wait %s", timeout, req.ID)
				a.ui.Result(askJSON(req, turn, saved, exitcode.TimedOut))
				return silent(exitcode.TimedOut)
			}
			continue
		}
		s, failed := savePending(ctx, a, j, req, saver)
		saved = append(saved, s...)
		downloadFailed = downloadFailed || failed
		break
	}
	codes := []int{}
	for _, o := range req.Outcomes {
		codes = append(codes, exitcode.ForOutcome(o))
	}
	if req.Status == "failed" {
		codes = append(codes, exitcode.Failed)
	}
	if downloadFailed {
		codes = append(codes, exitcode.DownloadFailed)
	}
	code := exitcode.Worst(codes...)
	if code == exitcode.Expired {
		a.ui.Warn("It's taking longer than the server keeps watching. Get the result later with: supercool wait %s", req.ID)
	}
	// Finished only when nothing is left to fetch and the outcome is final:
	// a failed download, a timeout or an expiry stays outstanding for `wait`.
	switch code {
	case exitcode.OK, exitcode.Failed, exitcode.Stopped, exitcode.Credits, exitcode.Unknown:
		if len(req.Pending) == 0 {
			now := time.Now()
			req.Finished, req.FinishedAt = true, &now
			_ = j.SaveRequest(req)
		}
	}
	a.ui.Result(askJSON(req, turn, saved, code))
	return silent(code)
}

func titleOf(req *journal.Request, workID string) string {
	if t := req.Work[workID]; t != "" {
		return t
	}
	if workID == "" {
		return "your request"
	}
	return "work " + workID
}

// applyEntry records one update-log entry; true when the turn must be resent
// (the agent was busy after the client had been told "processing").
func applyEntry(a *app, req *journal.Request, e api.Entry) bool {
	if e.WorkID != "" && e.Title != "" {
		req.Work[e.WorkID] = e.Title
	}
	switch e.Kind {
	case "progress":
		a.ui.Status("%s: %s", titleOf(req, e.WorkID), e.Step)
	case "result":
		req.Outcomes[e.WorkID] = e.Outcome
		req.Pending = appendFiles(req.Pending, e.Files)
		if req.Results == nil {
			req.Results = map[string]string{}
		}
		if t := strings.TrimSpace(e.Text); t != "" {
			req.Results[e.WorkID] = t
		}
		switch e.Outcome {
		case "completed":
			a.ui.Success("Finished: %s", titleOf(req, e.WorkID))
		case "stalled_credits":
			a.ui.Warn("%s stopped partway: the account is out of credits. Add credits, then ask your agent to carry on.", titleOf(req, e.WorkID))
		case "failed":
			a.ui.Warn("%s failed.", titleOf(req, e.WorkID))
		}
		if strings.TrimSpace(e.Text) != "" {
			a.ui.Print("%s", strings.TrimSpace(e.Text))
		}
	case "state":
		req.Outcomes[e.WorkID] = e.Outcome
		switch e.Outcome {
		case "stopped":
			a.ui.Warn("%s was stopped.", titleOf(req, e.WorkID))
		case "unknown":
			a.ui.Warn("%s: %s", titleOf(req, e.WorkID), e.Text)
		}
	case "agent_reply":
		req.Status = "answered"
		req.Reply = e.Text
		req.Pending = appendFiles(req.Pending, e.Files)
		if e.Text != "" {
			a.ui.Print("%s", e.Text)
		}
	case "turn_failed":
		req.Status = "failed"
		a.ui.Warn("%s", e.Text)
	case "turn_busy":
		return true
	}
	return false
}

type savedFile struct {
	ID     string `json:"id"`
	WorkID string `json:"work_id"`
	Name   string `json:"file_name"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
}

// savePending downloads every pending file; saved ones leave the list.
func savePending(ctx context.Context, a *app, j *journal.Journal, req *journal.Request, saver *download.Saver) ([]savedFile, bool) {
	var saved []savedFile
	failed := false
	var left []api.File
	for _, f := range req.Pending {
		if req.Out == "-" {
			if req.Streamed || len(req.Pending) > 1 {
				a.ui.Warn("--out - takes exactly one file, and this work made more. Nothing is lost: supercool wait %s --out DIR", req.ID)
				failed = true
				left = append(left, f)
				continue
			}
			if err := download.Stream(ctx, f, os.Stdout); err != nil {
				a.ui.Warn("%v", err)
				failed = true
				left = append(left, f)
				continue
			}
			req.Streamed = true
			continue
		}
		dir := req.Out
		if dir == "" {
			dir = filepath.Join("supercool", download.Slug(req.Work[f.WorkID], f.WorkID))
		}
		a.ui.Status("Saving %s…", f.FileName)
		p, err := saver.Save(ctx, f, dir)
		if err != nil {
			a.ui.Warn("%v", err)
			failed = true
			left = append(left, f)
			continue
		}
		a.ui.Success("Saved %s", p)
		saved = append(saved, savedFile{ID: f.ID, WorkID: f.WorkID, Name: f.FileName, Kind: f.Kind, Path: p})
	}
	req.Pending = left
	_ = j.SaveRequest(req)
	return saved, failed
}

func askJSON(req *journal.Request, t *api.Turn, saved []savedFile, code int) map[string]any {
	var work []map[string]string
	ids := make([]string, 0, len(req.Work))
	for id := range req.Work {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		work = append(work, map[string]string{"work_id": id, "title": req.Work[id], "outcome": req.Outcomes[id],
			"text": req.Results[id]})
	}
	out := map[string]any{"request_id": req.ID, "status": req.Status, "reply": req.Reply, "work": work,
		"files": saved, "exit_code": code}
	if t != nil && len(saved) == 0 && len(t.Files) > 0 {
		out["links"] = t.Files
	}
	if len(req.Pending) > 0 {
		out["pending_files"] = req.Pending
	}
	return out
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
