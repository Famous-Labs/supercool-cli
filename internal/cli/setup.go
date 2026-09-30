package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Famous-Labs/supercool-cli/internal/exitcode"
	"github.com/Famous-Labs/supercool-cli/internal/version"
)

const (
	skillsRepo = "Famous-Labs/supercool-skills"
	mcpURL     = "https://mcp.supercool.com/mcp"
)

// skillsTarball is the skills repo's main branch (a var so tests can point
// it at a local server).
var skillsTarball = "https://codeload.github.com/" + skillsRepo + "/tar.gz/refs/heads/main"

// skillDirs is where each coding agent looks for skills.
func skillDirs(agent string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch agent {
	case "claude":
		return filepath.Join(home, ".claude", "skills"), nil
	case "cursor":
		return filepath.Join(home, ".cursor", "skills"), nil
	case "codex":
		return filepath.Join(home, ".codex", "skills"), nil
	}
	return "", fmt.Errorf("unknown agent %q (use claude, cursor or codex)", agent)
}

func detectAgents() []string {
	home, _ := os.UserHomeDir()
	var found []string
	for _, a := range []string{"claude", "cursor", "codex"} {
		if _, err := exec.LookPath(a); err == nil {
			found = append(found, a)
			continue
		}
		if st, err := os.Stat(filepath.Join(home, "."+a)); err == nil && st.IsDir() {
			found = append(found, a)
		}
	}
	return found
}

func setupCmd() *cobra.Command {
	var withMCP bool
	cmd := &cobra.Command{
		Use:   "setup [claude|cursor|codex]",
		Short: "Teach your coding agent to use SuperCool (installs the skills)",
		Long: `Installs the SuperCool skills into Claude Code, Cursor or Codex, so they know when
and how to hand work to your agent. With no argument, sets up every one found.

  supercool setup claude
  supercool setup claude --mcp   # also add the SuperCool MCP server`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"claude", "cursor", "codex"},
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			agents := args
			if len(agents) == 0 {
				if agents = detectAgents(); len(agents) == 0 {
					return exitcode.New(exitcode.Error, "no coding agent found; run: supercool setup claude|cursor|codex")
				}
			}
			ctx := cmd.Context()
			done := map[string][]string{}
			for _, agent := range agents {
				names, err := installSkills(ctx, a, agent)
				if err != nil {
					return err
				}
				done[agent] = names
				if withMCP {
					addMCP(a, agent)
				}
			}
			if !a.tokens.LoggedIn() {
				a.ui.Info("Next: supercool login")
			}
			a.ui.Result(map[string]any{"installed": done})
			return nil
		},
	}
	cmd.Flags().BoolVar(&withMCP, "mcp", false, "also add the SuperCool MCP server to the agent")
	return cmd
}

// installSkills uses the agent's own plugin installer when it has one
// (Claude Code), else copies the skill folders into its skills directory.
func installSkills(ctx context.Context, a *app, agent string) ([]string, error) {
	if agent == "claude" {
		if claude, err := exec.LookPath("claude"); err == nil {
			a.ui.Status("Adding the SuperCool plugin to Claude Code…")
			add := exec.CommandContext(ctx, claude, "plugin", "marketplace", "add", skillsRepo)
			inst := exec.CommandContext(ctx, claude, "plugin", "install", "supercool@supercool")
			if add.Run() == nil && inst.Run() == nil {
				a.ui.Success("Claude Code: installed the SuperCool plugin (restart Claude Code to load it)")
				return []string{"supercool@supercool"}, nil
			}
		}
	}
	dest, err := skillDirs(agent)
	if err != nil {
		return nil, err
	}
	a.ui.Status("Downloading the SuperCool skills…")
	names, err := extractSkills(ctx, dest)
	if err != nil {
		return nil, fmt.Errorf("couldn't install the skills for %s: %w", agent, err)
	}
	a.ui.Success("%s: installed %s into %s", agent, strings.Join(names, ", "), dest)
	return names, nil
}

// extractSkills downloads the skills repo and copies each skills/<name>/
// folder holding a SKILL.md into dest/<name>. Only regular files inside those
// folders are written; nothing can land outside dest.
func extractSkills(ctx context.Context, dest string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, skillsTarball, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: %s", resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, err
	}
	type file struct {
		rel  string
		data []byte
	}
	bySkill := map[string][]file{}
	hasSkill := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.Split(filepath.ToSlash(h.Name), "/")
		// <repo-root>/skills/<skill>/<path…>
		if len(parts) < 4 || parts[1] != "skills" || strings.HasPrefix(parts[2], ".") {
			continue
		}
		parts = parts[1:] // drop the repo root: skills/<skill>/<path…> -> <skill> is parts[1]
		rel := strings.Join(parts[2:], "/")
		if strings.Contains(rel, "..") || h.Size > 5<<20 {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 5<<20))
		if err != nil {
			return nil, err
		}
		bySkill[parts[1]] = append(bySkill[parts[1]], file{rel, data})
		if rel == "SKILL.md" {
			hasSkill[parts[1]] = true
		}
	}
	var names []string
	for skill, files := range bySkill {
		if !hasSkill[skill] {
			continue
		}
		root := filepath.Join(dest, skill)
		if err := os.RemoveAll(root); err != nil {
			return nil, err
		}
		for _, f := range files {
			p := filepath.Join(root, filepath.FromSlash(f.rel))
			if !strings.HasPrefix(p, root+string(os.PathSeparator)) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(p, f.data, 0o644); err != nil {
				return nil, err
			}
		}
		names = append(names, skill)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills found in %s", skillsRepo)
	}
	return names, nil
}

func addMCP(a *app, agent string) {
	if agent == "claude" {
		if claude, err := exec.LookPath("claude"); err == nil {
			if exec.Command(claude, "mcp", "add", "--transport", "http", "supercool", mcpURL).Run() == nil {
				a.ui.Success("Claude Code: added the SuperCool MCP server (it asks you to sign in on first use)")
				return
			}
		}
	}
	a.ui.Info("%s: add an MCP server named \"supercool\" with the URL %s (it signs in with your SuperCool account).", agent, mcpURL)
}
