package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/Famous-Labs/supercool-cli/internal/api"
	"github.com/Famous-Labs/supercool-cli/internal/config"
	"github.com/Famous-Labs/supercool-cli/internal/exitcode"
	"github.com/Famous-Labs/supercool-cli/internal/version"
)

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeURL(cfg *api.CLIConfig, redirect, state, verifier string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"scope":                 {cfg.Scope},
		"resource":              {cfg.Resource},
	}
	return cfg.AuthorizeEndpoint + "?" + q.Encode()
}

// OpenBrowser opens a URL in the user's browser.
func OpenBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func postToken(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr tokenResponse
	_ = json.Unmarshal(data, &tr)
	if resp.StatusCode >= 300 || tr.AccessToken == "" {
		var wrapped struct {
			Detail tokenResponse `json:"detail"`
		}
		_ = json.Unmarshal(data, &wrapped)
		reason := firstNonEmpty(tr.Description, tr.Error, wrapped.Detail.Description, wrapped.Detail.Error, resp.Status)
		return nil, exitcode.New(exitcode.LoginNeeded, "login refused: "+reason)
	}
	return &tr, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// subject reads the token's sub claim (no verification: it only names the
// local state directory).
func subject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(data, &claims)
	return claims.Sub
}

func exchange(ctx context.Context, cfg *api.CLIConfig, apiURL, code, redirect, verifier string) (*Credentials, error) {
	tr, err := postToken(ctx, cfg.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
		"resource":      {cfg.Resource},
	})
	if err != nil {
		return nil, err
	}
	return &Credentials{
		APIURL: apiURL, ClientID: cfg.ClientID, TokenEndpoint: cfg.TokenEndpoint, RevokeURL: cfg.RevocationEndpoint,
		Resource: cfg.Resource, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken,
		ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second), Subject: subject(tr.AccessToken),
	}, nil
}

// LoginBrowser runs the loopback flow (RFC 8252): listen on a free port,
// open the browser, catch the redirect, check state, exchange the code.
func LoginBrowser(ctx context.Context, cfg *api.CLIConfig, apiURL string, out io.Writer, noOpen bool) (*Credentials, error) {
	ln, host, err := listenLoopback()
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirect := fmt.Sprintf("http://%s:%d/callback", host, port)
	state, verifier := randomString(24), randomString(48)
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("the sign-in response didn't match this login (state mismatch); run `supercool login` again")
		case q.Get("error") != "":
			res.err = exitcode.New(exitcode.LoginNeeded, "sign-in cancelled ("+q.Get("error")+")")
		case q.Get("code") == "":
			res.err = errors.New("the sign-in response had no code")
		default:
			res.code = q.Get("code")
		}
		msg := "You're signed in to the SuperCool CLI. You can close this tab."
		if res.err != nil {
			msg = "Sign-in didn't complete: " + res.err.Error()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>SuperCool CLI</title><body style="font-family:system-ui;background:#080a0c;color:#fff;display:flex;align-items:center;justify-content:center;height:100vh;margin:0"><p>%s</p>`, html.EscapeString(msg))
		select {
		case done <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	link := authorizeURL(cfg, redirect, state, verifier)
	fmt.Fprintln(out, "Opening your browser to sign in to SuperCool…")
	if noOpen || OpenBrowser(link) != nil {
		fmt.Fprintln(out, "Open this link to sign in:")
	} else {
		fmt.Fprintln(out, "If it didn't open, use this link:")
	}
	fmt.Fprintln(out, "  "+link)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	select {
	case res := <-done:
		if res.err != nil {
			return nil, res.err
		}
		return exchange(ctx, cfg, apiURL, res.code, redirect, verifier)
	case <-ctx.Done():
		return nil, exitcode.New(exitcode.LoginNeeded, "sign-in timed out")
	}
}

func listenLoopback() (net.Listener, string, error) {
	if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		return ln, "127.0.0.1", nil
	}
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return nil, "", fmt.Errorf("can't listen on a local port for the sign-in redirect: %w (try --no-browser)", err)
	}
	return ln, "[::1]", nil
}

// ParsePasted reads what the code page gave (sc1.<code>.<state>) or a whole
// pasted callback URL. Returns (code, state).
func ParsePasted(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", "", err
		}
		if e := u.Query().Get("error"); e != "" {
			return "", "", exitcode.New(exitcode.LoginNeeded, "sign-in cancelled ("+e+")")
		}
		return u.Query().Get("code"), u.Query().Get("state"), nil
	}
	if !strings.HasPrefix(s, "sc1.") {
		return "", "", errors.New("that doesn't look like a SuperCool sign-in code (it starts with sc1.)")
	}
	rest := strings.TrimPrefix(s, "sc1.")
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return "", "", errors.New("that sign-in code is incomplete; copy the whole thing")
	}
	return rest[:i], rest[i+1:], nil
}

// LoginPaste is the SSH / headless flow: the browser (on any machine) ends on
// a SuperCool page showing sc1.<code>.<state>, which the user pastes here.
func LoginPaste(ctx context.Context, cfg *api.CLIConfig, apiURL string, in io.Reader, out io.Writer) (*Credentials, error) {
	state, verifier := randomString(24), randomString(48)
	link := authorizeURL(cfg, cfg.CodeRedirect, state, verifier)
	fmt.Fprintln(out, "Open this link in a browser on any device and sign in:")
	fmt.Fprintln(out, "  "+link)
	fmt.Fprint(out, "Then paste the code it shows here: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return nil, errors.New("no code entered")
	}
	code, gotState, err := ParsePasted(line)
	if err != nil {
		return nil, err
	}
	if gotState != state {
		return nil, errors.New("that code belongs to a different login attempt (state mismatch); run `supercool login --no-browser` again")
	}
	return exchange(ctx, cfg, apiURL, code, cfg.CodeRedirect, verifier)
}

// Revoke tells the server to drop the refresh token (logout).
func Revoke(ctx context.Context, c *Credentials) {
	if c == nil || c.RevokeURL == "" || c.RefreshToken == "" {
		return
	}
	form := url.Values{"token": {c.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {c.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req); err == nil {
		resp.Body.Close()
	}
}

// ── the token source used by the API client ──────────────────────────────────

// Source serves the bearer token: a personal access token as-is, or the
// stored OAuth login, refreshed when stale. Refresh is locked across CLI
// processes: refresh tokens rotate, so two terminals refreshing at once
// would otherwise log one of them out.
type Source struct {
	PAT   string
	Key   string
	creds *Credentials
}

// NewSource loads the stored login (nil creds when logged out).
func NewSource(pat, key string) (*Source, error) {
	s := &Source{PAT: pat, Key: key}
	if pat == "" {
		c, err := Load(key)
		if err != nil {
			return nil, err
		}
		s.creds = c
	}
	return s, nil
}

// LoggedIn reports whether there is anything to authenticate with.
func (s *Source) LoggedIn() bool { return s.PAT != "" || (s.creds != nil && s.creds.AccessToken != "") }

// Identity names the authenticated connection for local state: the PAT's
// hash, or the login's user + client.
func (s *Source) Identity() string {
	if s.PAT != "" {
		return "pat:" + config.Hash(s.PAT)
	}
	if s.creds != nil {
		return "oauth:" + s.creds.Subject + ":" + s.creds.ClientID
	}
	return "anonymous"
}

func (s *Source) Token(ctx context.Context) (string, error) {
	if s.PAT != "" {
		return s.PAT, nil
	}
	if s.creds == nil || s.creds.AccessToken == "" {
		return "", exitcode.New(exitcode.LoginNeeded, "not logged in: run `supercool login` (or set SUPERCOOL_TOKEN)")
	}
	if time.Until(s.creds.ExpiresAt) < time.Minute {
		if _, err := s.Refresh(ctx, s.creds.AccessToken); err != nil {
			return "", err
		}
	}
	return s.creds.AccessToken, nil
}

func lockPath(key string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	locks := filepath.Join(dir, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(locks, "refresh-"+config.Hash(key)+".lock"), nil
}

func (s *Source) Refresh(ctx context.Context, rejected string) (bool, error) {
	if s.PAT != "" || s.creds == nil || s.creds.RefreshToken == "" {
		return false, nil
	}
	p, err := lockPath(s.Key)
	if err != nil {
		return false, err
	}
	fl := flock.New(p)
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ok, err := fl.TryLockContext(lctx, 200*time.Millisecond)
	if err != nil || !ok {
		return false, fmt.Errorf("another supercool process is refreshing the login; try again")
	}
	defer fl.Unlock()
	// Another process may have refreshed while we waited: use its token.
	if fresh, lerr := Load(s.Key); lerr == nil && fresh != nil && fresh.AccessToken != rejected &&
		time.Until(fresh.ExpiresAt) > time.Minute {
		s.creds = fresh
		return true, nil
	}
	tr, err := postToken(ctx, s.creds.TokenEndpoint, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {s.creds.RefreshToken},
		"client_id": {s.creds.ClientID}, "resource": {s.creds.Resource},
	})
	if err != nil {
		return false, exitcode.New(exitcode.LoginNeeded, "your login expired: run `supercool login`")
	}
	next := *s.creds
	next.AccessToken, next.RefreshToken = tr.AccessToken, tr.RefreshToken
	next.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if err := Save(s.Key, &next); err != nil {
		return false, err
	}
	s.creds = &next
	return true, nil
}

// Credentials returns the loaded login (nil for a PAT or when logged out).
func (s *Source) Credentials() *Credentials { return s.creds }
