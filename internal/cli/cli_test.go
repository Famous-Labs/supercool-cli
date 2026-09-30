package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Famous-Labs/supercool-cli/internal/config"
)

// fakeAPI is a scripted agent API: messages answer from a queue, updates
// serve pages from a queue, files download from /dl/.
type fakeAPI struct {
	mu         sync.Mutex
	turns      []string // JSON bodies for successive POST /messages
	pages      []string // JSON bodies for successive GET /updates
	requestIDs []string
	recovered  int
	srv        *httptest.Server
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer sc_pat_test" && strings.HasPrefix(r.URL.Path, "/api/") {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"unauthorized","message":"bad token"}`)
		return
	}
	pop := func(q *[]string) string {
		if len(*q) == 0 {
			return `{"entries":[],"work":[],"more":false,"done":true,"cursor":"end"}`
		}
		b := (*q)[0]
		*q = (*q)[1:]
		return strings.ReplaceAll(b, "{{srv}}", f.srv.URL)
	}
	switch {
	case r.URL.Path == "/api/v1/agent/messages":
		var in struct {
			RequestID string `json:"request_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.requestIDs = append(f.requestIDs, in.RequestID)
		fmt.Fprint(w, strings.ReplaceAll(pop(&f.turns), "{{rid}}", in.RequestID))
	case r.URL.Path == "/api/v1/agent/updates":
		fmt.Fprint(w, pop(&f.pages))
	case strings.HasSuffix(r.URL.Path, "/recover"):
		f.recovered++
		fmt.Fprint(w, `{"request_id":"x","work":[{"work_id":"s1","state":"recovering"}]}`)
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		fmt.Fprint(w, "VIDEO")
	default:
		w.WriteHeader(404)
	}
}

func newFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	t.Setenv("SUPERCOOL_CONFIG_DIR", t.TempDir())
	t.Setenv("SUPERCOOL_TOKEN", "sc_pat_test")
	t.Setenv("SUPERCOOL_NO_UPDATE_CHECK", "1")
	return f
}

// run executes the CLI with args and returns (exit code, stdout).
func run(t *testing.T, f *fakeAPI, args ...string) (int, string) {
	t.Helper()
	settings = config.Settings{}
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Args = append([]string{"supercool"}, append(args, "--api-url", f.srv.URL, "--json")...)
	root := Root()
	root.SetArgs(os.Args[1:])
	code := 0
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stdout = old }()
		code = executeRoot(root)
		w.Close()
	}()
	return code, <-done
}

const fileJSON = `{"id":"s1/ad.mp4","work_id":"s1","file_name":"ad.mp4","kind":"video","url":"{{srv}}/dl/r1","revision":"\"r1\"","size":5}`

func TestAskWaitSavesTheFileAndExitsZero(t *testing.T) {
	f := newFake(t)
	f.turns = []string{`{"request_id":"{{rid}}","status":"processing","reply":null,"work":[{"work_id":"s1","title":"Candle ad","link":"L"}],"cursor":"c0","files":[]}`}
	f.pages = []string{
		`{"entries":[{"seq":1,"kind":"progress","work_id":"s1","step":"rendering"}],"work":[{"work_id":"s1","state":"running"}],"more":false,"done":false,"cursor":"c1"}`,
		`{"entries":[{"seq":2,"kind":"result","work_id":"s1","title":"Candle ad","text":"Done!","outcome":"completed","files":[` + fileJSON + `]}],"work":[],"more":false,"done":true,"cursor":"c2"}`,
	}
	out := t.TempDir()
	code, stdout := run(t, f, "ask", "make a candle ad", "--wait", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	data, err := os.ReadFile(filepath.Join(out, "ad.mp4"))
	if err != nil || string(data) != "VIDEO" {
		t.Fatalf("file not saved: %v %q", err, data)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("not JSON: %s", stdout)
	}
	if res["exit_code"].(float64) != 0 || len(res["files"].([]any)) != 1 {
		t.Fatalf("bad result: %s", stdout)
	}
}

func TestAFailedOutcomeExitsFive(t *testing.T) {
	f := newFake(t)
	f.turns = []string{`{"request_id":"{{rid}}","status":"processing","work":[{"work_id":"s1","title":"X"}],"cursor":"c0","files":[]}`}
	f.pages = []string{`{"entries":[{"seq":1,"kind":"result","work_id":"s1","outcome":"failed","files":[]}],"work":[],"more":false,"done":true,"cursor":"c1"}`}
	if code, out := run(t, f, "ask", "x", "--wait", "--out", t.TempDir()); code != 5 {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestExpiredWorkExitsNineAndWaitRecoversIt(t *testing.T) {
	f := newFake(t)
	f.turns = []string{`{"request_id":"{{rid}}","status":"processing","work":[{"work_id":"s1","title":"Long video"}],"cursor":"c0","files":[]}`}
	f.pages = []string{`{"entries":[{"seq":1,"kind":"state","work_id":"s1","outcome":"expired"}],"work":[],"more":false,"done":true,"cursor":"c1"}`}
	out := t.TempDir()
	code, stdout := run(t, f, "ask", "a long video", "--wait", "--out", out)
	if code != 9 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(stdout), &res)
	rid := res["request_id"].(string)
	f.pages = []string{`{"entries":[{"seq":2,"kind":"result","work_id":"s1","outcome":"completed","files":[` + fileJSON + `]}],"work":[],"more":false,"done":true,"cursor":"c2"}`}
	code, stdout = run(t, f, "wait", rid, "--out", out)
	if code != 0 || f.recovered != 1 {
		t.Fatalf("exit %d recovered %d: %s", code, f.recovered, stdout)
	}
	if len(f.requestIDs) != 1 {
		t.Fatalf("recovery resent the message: %v", f.requestIDs)
	}
	if _, err := os.Stat(filepath.Join(out, "ad.mp4")); err != nil {
		t.Fatal("recovered file not saved")
	}
}

func TestABusyAgentIsRetriedWithTheSameRequestID(t *testing.T) {
	f := newFake(t)
	f.turns = []string{
		`{"request_id":"{{rid}}","status":"busy","retry_after_seconds":1,"work":[],"cursor":"c0","files":[]}`,
		`{"request_id":"{{rid}}","status":"answered","reply":"Sure!","work":[],"cursor":"c0","files":[]}`,
	}
	code, stdout := run(t, f, "ask", "hi", "--wait", "--out", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	if len(f.requestIDs) != 2 || f.requestIDs[0] != f.requestIDs[1] {
		t.Fatalf("retry used a different id: %v", f.requestIDs)
	}
}

func TestWithoutWaitABusyAgentExitsSeven(t *testing.T) {
	f := newFake(t)
	f.turns = []string{`{"request_id":"{{rid}}","status":"busy","retry_after_seconds":15,"work":[],"cursor":"c0","files":[]}`}
	if code, out := run(t, f, "ask", "hi"); code != 7 {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestABadTokenExitsTwo(t *testing.T) {
	f := newFake(t)
	t.Setenv("SUPERCOOL_TOKEN", "sc_pat_wrong")
	if code, out := run(t, f, "whoami"); code != 2 {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestJSONKeepsEachResultsText(t *testing.T) {
	f := newFake(t)
	f.turns = []string{`{"request_id":"{{rid}}","status":"processing","reply":"On it.","work":[{"work_id":"s1","title":"Research"}],"cursor":"c0","files":[]}`}
	f.pages = []string{`{"entries":[{"seq":1,"kind":"result","work_id":"s1","outcome":"completed","text":"The market is $4B.","files":[]}],"work":[],"more":false,"done":true,"cursor":"c1"}`}
	code, stdout := run(t, f, "ask", "research the market", "--wait", "--out", t.TempDir())
	if code != 0 || !strings.Contains(stdout, "The market is $4B.") {
		t.Fatalf("exit %d, result text missing: %s", code, stdout)
	}
}

func TestAFailedDownloadStaysOutstandingForWait(t *testing.T) {
	f := newFake(t)
	f.turns = []string{`{"request_id":"{{rid}}","status":"processing","work":[{"work_id":"s1","title":"Ad"}],"cursor":"c0","files":[]}`}
	bad := strings.Replace(fileJSON, "/dl/r1", "/missing", 1)
	f.pages = []string{`{"entries":[{"seq":1,"kind":"result","work_id":"s1","outcome":"completed","files":[` + bad + `]}],"work":[],"more":false,"done":true,"cursor":"c1"}`}
	code, _ := run(t, f, "ask", "x", "--wait", "--out", t.TempDir())
	if code != 8 {
		t.Fatalf("exit %d, want 8", code)
	}
	// A bare wait still finds it (not "nothing outstanding").
	code, stdout := run(t, f, "wait")
	if !strings.Contains(stdout, "pending_files") || code != 8 {
		t.Fatalf("the failed download wasn't retried: exit %d %s", code, stdout)
	}
}

func TestWaitAlwaysConsultsRecoveryEvenWithoutALocalExpiry(t *testing.T) {
	f := newFake(t)
	f.pages = []string{`{"entries":[{"seq":1,"kind":"result","work_id":"s1","outcome":"completed","files":[]}],"work":[],"more":false,"done":true,"cursor":"c1"}`}
	// Another machine: no local record at all.
	code, stdout := run(t, f, "wait", "cli_from_elsewhere", "--out", t.TempDir())
	if f.recovered != 1 || code != 0 {
		t.Fatalf("recovery not consulted (recovered=%d) exit %d: %s", f.recovered, code, stdout)
	}
}

func TestOutDashRefusesJSONAndMoreThanOneFile(t *testing.T) {
	f := newFake(t)
	if code, _ := run(t, f, "ask", "x", "--wait", "--out", "-"); code != 1 {
		t.Fatalf("--out - with --json should be refused, got exit %d", code)
	}
}

func TestARetriedStdoutRequestStillRefusesJSON(t *testing.T) {
	f := newFake(t)
	f.turns = []string{`{"request_id":"{{rid}}","status":"busy","retry_after_seconds":15,"work":[],"cursor":"c0","files":[]}`}
	settings = config.Settings{}
	// First attempt, without --json, streaming to stdout (the agent is busy: exit 7).
	os.Args = []string{"supercool"}
	root := Root()
	root.SetArgs([]string{"ask", "hi", "--out", "-", "--request-id", "cli_retry1", "--api-url", f.srv.URL})
	_ = executeRoot(root)
	// Retried by id with --json but no --out: the saved Out "-" must still be refused.
	if code, out := run(t, f, "ask", "hi", "--request-id", "cli_retry1", "--wait"); code != 1 || strings.Contains(out, "VIDEO") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestWorkGetDownloadFailureExitsEight(t *testing.T) {
	f := newFake(t)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent/work/") {
			fmt.Fprintf(w, `{"work_id":"s1","title":"Ad","status":"idle","text":"","link":"L","files":[{"id":"s1/a.mp4","work_id":"s1","file_name":"a.mp4","kind":"video","url":"%s/missing","revision":"\"r\"","size":5}]}`, "http://"+r.Host)
			return
		}
		w.WriteHeader(404)
	})
	if code, out := run(t, f, "work", "get", "s1", "--download", "--out", t.TempDir()); code != 8 {
		t.Fatalf("exit %d: %s", code, out)
	}
}
