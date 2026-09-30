// Package ui prints for people (a live line on a terminal, one line per
// event when piped) or for programs (--json: only the final JSON).
package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// UI writes human output to Err and results to Out.
type UI struct {
	Out   io.Writer
	Err   io.Writer
	JSON  bool
	Quiet bool
	Color bool
	tty   bool
	mu    sync.Mutex
	live  bool
}

// New detects whether stderr is a terminal.
func New(jsonMode, quiet, noColor bool) *UI {
	tty := term.IsTerminal(int(os.Stderr.Fd()))
	return &UI{Out: os.Stdout, Err: os.Stderr, JSON: jsonMode, Quiet: quiet, Color: tty && !noColor, tty: tty}
}

func (u *UI) paint(code, s string) string {
	if !u.Color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (u *UI) clearLive() {
	if u.live {
		fmt.Fprint(u.Err, "\r\x1b[2K")
		u.live = false
	}
}

// Status is a transient progress line (overwritten on a terminal; its own
// line when piped; nothing in JSON or quiet mode).
func (u *UI) Status(format string, a ...any) {
	if u.JSON || u.Quiet {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	msg := fmt.Sprintf(format, a...)
	if u.tty {
		w, _, err := term.GetSize(int(os.Stderr.Fd()))
		if err == nil && w > 4 && len(msg) > w-2 {
			msg = msg[:w-3] + "…"
		}
		fmt.Fprint(u.Err, "\r\x1b[2K"+u.paint("2", msg))
		u.live = true
		return
	}
	fmt.Fprintln(u.Err, msg)
}

// Info is a lasting line for people.
func (u *UI) Info(format string, a ...any) {
	if u.JSON || u.Quiet {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearLive()
	fmt.Fprintf(u.Err, format+"\n", a...)
}

// Success is a lasting line with a check mark.
func (u *UI) Success(format string, a ...any) { u.Info(u.paint("32", "✓")+" "+format, a...) }

// Warn is a lasting warning line (shown even in quiet mode, never in JSON).
func (u *UI) Warn(format string, a ...any) {
	if u.JSON {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearLive()
	fmt.Fprintf(u.Err, u.paint("33", "!")+" "+format+"\n", a...)
}

// Print writes a result line to stdout (never in JSON mode).
func (u *UI) Print(format string, a ...any) {
	if u.JSON {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearLive()
	fmt.Fprintf(u.Out, format+"\n", a...)
}

// Result writes the final JSON (JSON mode only).
func (u *UI) Result(v any) {
	if !u.JSON {
		return
	}
	enc := json.NewEncoder(u.Out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// Done ends any live line.
func (u *UI) Done() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearLive()
}

// Indent prefixes every line of s.
func Indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
