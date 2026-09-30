// Package api is the client for the SuperCool agent API (/api/v1/agent).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Famous-Labs/supercool-cli/internal/exitcode"
	"github.com/Famous-Labs/supercool-cli/internal/version"
)

// TokenSource supplies the bearer token, and can refresh it once after a 401.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Refresh is called after a 401; false means there's nothing to refresh
	// with (a personal access token, or no login).
	Refresh(ctx context.Context, rejected string) (bool, error)
}

// Client talks to one API base URL.
type Client struct {
	BaseURL string // e.g. https://api.supercool.sh
	Tokens  TokenSource
	HTTP    *http.Client
}

// New returns a client with sensible timeouts (long-polls run up to ~45s).
func New(base string, tokens TokenSource) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), Tokens: tokens, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Error is a non-2xx answer from the API.
type Error struct {
	Status     int
	Code       string `json:"error"`
	Message    string `json:"message"`
	RetryAfter int
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("the API answered %d", e.Status)
}

// ExitCode maps an API error to the CLI's exit code.
func (e *Error) ExitCode() int {
	switch {
	case e.Status == 401:
		return exitcode.LoginNeeded
	case e.Status == 429:
		return exitcode.RateLimited
	case e.Status == 402 || e.Code == "insufficient_credits":
		return exitcode.Credits
	}
	return exitcode.Error
}

// CodeFor returns the exit code for any error the client returns.
func CodeFor(err error) int {
	var ae *Error
	if errors.As(err, &ae) {
		return ae.ExitCode()
	}
	var ce *exitcode.Coded
	if errors.As(err, &ce) {
		return ce.Code
	}
	return exitcode.Error
}

func (c *Client) url(path string, q url.Values) string {
	u := c.BaseURL + "/api/v1/agent" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// Do sends a request (JSON body when in != nil), decodes JSON into out.
// A 401 triggers one token refresh and retry.
func (c *Client) Do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.url(path, q), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", version.UserAgent())
		req.Header.Set("Accept", "application/json")
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		var tok string
		if c.Tokens != nil {
			if tok, err = c.Tokens.Token(ctx); err != nil {
				return err
			}
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == 401 && attempt == 0 && c.Tokens != nil && tok != "" {
			if ok, rerr := c.Tokens.Refresh(ctx, tok); rerr == nil && ok {
				continue
			}
		}
		if resp.StatusCode >= 300 {
			e := &Error{Status: resp.StatusCode}
			_ = json.Unmarshal(data, e)
			if ra, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil {
				e.RetryAfter = ra
			}
			if e.Message == "" {
				var d struct {
					Detail any `json:"detail"`
				}
				if json.Unmarshal(data, &d) == nil && d.Detail != nil {
					e.Message = fmt.Sprint(d.Detail)
				}
			}
			return e
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
	return &Error{Status: 401, Message: "not logged in: run `supercool login`"}
}

// CLIConfig needs no login.
func (c *Client) CLIConfig(ctx context.Context) (*CLIConfig, error) {
	var out CLIConfig
	saved := c.Tokens
	c.Tokens = nil
	defer func() { c.Tokens = saved }()
	return &out, c.Do(ctx, http.MethodGet, "/cli-config", nil, nil, &out)
}

func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	return &out, c.Do(ctx, http.MethodGet, "/me", nil, nil, &out)
}

func (c *Client) SendMessage(ctx context.Context, m MessageIn) (*Turn, error) {
	var out Turn
	return &out, c.Do(ctx, http.MethodPost, "/messages", nil, m, &out)
}

// Updates long-polls the update log. requestID scopes it to one message;
// an empty cursor with a request id starts where that message started.
func (c *Client) Updates(ctx context.Context, cursor, requestID string, wait int) (*Poll, error) {
	q := url.Values{"wait": {strconv.Itoa(wait)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if requestID != "" {
		q.Set("request_id", requestID)
	}
	var out Poll
	return &out, c.Do(ctx, http.MethodGet, "/updates", q, nil, &out)
}

func (c *Client) Recover(ctx context.Context, requestID string) (*Recovery, error) {
	var out Recovery
	return &out, c.Do(ctx, http.MethodPost, "/turns/"+url.PathEscape(requestID)+"/recover", nil, nil, &out)
}

func (c *Client) WorkList(ctx context.Context, limit int) ([]WorkListItem, error) {
	var out struct {
		Work []WorkListItem `json:"work"`
	}
	err := c.Do(ctx, http.MethodGet, "/work", url.Values{"limit": {strconv.Itoa(limit)}}, nil, &out)
	return out.Work, err
}

func (c *Client) Work(ctx context.Context, id string, fromChar int) (*Work, error) {
	var out Work
	return &out, c.Do(ctx, http.MethodGet, "/work/"+url.PathEscape(id), url.Values{"from_char": {strconv.Itoa(fromChar)}}, nil, &out)
}

// FileRecord fetches a file's current record and a fresh link pinned to
// its current revision.
func (c *Client) FileRecord(ctx context.Context, fileID string) (*File, error) {
	var out File
	parts := strings.Split(fileID, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return &out, c.Do(ctx, http.MethodGet, "/files/"+strings.Join(parts, "/"), nil, nil, &out)
}

func (c *Client) CreateUpload(ctx context.Context, filename, contentType string, size int64) (*Upload, error) {
	var out Upload
	in := map[string]any{"filename": filename, "content_type": contentType, "size": size}
	return &out, c.Do(ctx, http.MethodPost, "/uploads", nil, in, &out)
}

func (c *Client) CompleteUpload(ctx context.Context, id string) (*Upload, error) {
	var out Upload
	return &out, c.Do(ctx, http.MethodPost, "/uploads/"+url.PathEscape(id)+"/complete", nil, nil, &out)
}
