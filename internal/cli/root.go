// Package cli wires the supercool commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Famous-Labs/supercool-cli/internal/api"
	"github.com/Famous-Labs/supercool-cli/internal/auth"
	"github.com/Famous-Labs/supercool-cli/internal/config"
	"github.com/Famous-Labs/supercool-cli/internal/exitcode"
	"github.com/Famous-Labs/supercool-cli/internal/journal"
	"github.com/Famous-Labs/supercool-cli/internal/ui"
	"github.com/Famous-Labs/supercool-cli/internal/version"
)

var settings config.Settings

// app is what a command needs, built after flags are parsed.
type app struct {
	s      *config.Settings
	ui     *ui.UI
	tokens *auth.Source
	client *api.Client
}

func newApp() (*app, error) {
	settings.Resolve()
	src, err := auth.NewSource(settings.Token, auth.Key(settings.Profile, settings.APIURL))
	if err != nil {
		return nil, err
	}
	return &app{
		s:      &settings,
		ui:     ui.New(settings.JSON, settings.Quiet, settings.NoColor),
		tokens: src,
		client: api.New(settings.APIURL, src),
	}, nil
}

// journal opens this connection's local state (needs a login or a token).
func (a *app) journal() (*journal.Journal, error) {
	if !a.tokens.LoggedIn() {
		return nil, exitcode.New(exitcode.LoginNeeded, "not logged in: run `supercool login` (or set SUPERCOOL_TOKEN)")
	}
	return journal.Open(a.s.APIURL, a.tokens.Identity())
}

func (a *app) requireLogin() error {
	if !a.tokens.LoggedIn() {
		return exitcode.New(exitcode.LoginNeeded, "not logged in: run `supercool login` (or set SUPERCOOL_TOKEN)")
	}
	return nil
}

// Root builds the command tree.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "supercool",
		Short: "Your SuperCool agent in the terminal",
		Long: `Ask your SuperCool agent for a video, a website, a deck, research, images and more,
and get the finished files on disk. Same agent as on calls, texts and the web app.

  supercool login
  supercool ask "a 15s vertical ad for my candle shop, warm and cozy" --wait`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
	}
	root.SetVersionTemplate(version.String() + "\n")
	pf := root.PersistentFlags()
	pf.BoolVar(&settings.JSON, "json", false, "print machine-readable JSON (for scripts and coding agents)")
	pf.BoolVarP(&settings.Quiet, "quiet", "q", false, "print only results")
	pf.BoolVar(&settings.NoColor, "no-color", false, "no colors")
	pf.StringVar(&settings.Token, "token", "", "a personal access token (or set SUPERCOOL_TOKEN)")
	pf.StringVar(&settings.Profile, "profile", "", "which saved login to use (default \"default\")")
	pf.StringVar(&settings.APIURL, "api-url", "", "API address (default https://api.supercool.sh)")
	_ = pf.MarkHidden("api-url")

	root.AddCommand(loginCmd(), logoutCmd(), whoamiCmd(), askCmd(), waitCmd(), workCmd(), tokenCmd(),
		setupCmd(), versionCmd(), updateCmd())
	// At most once a day, on a terminal, never in JSON mode: a newer version?
	root.PersistentPostRun = func(cmd *cobra.Command, _ []string) {
		if settings.JSON || settings.Quiet || cmd.Name() == "update" || cmd.Name() == "version" ||
			!term.IsTerminal(int(os.Stderr.Fd())) {
			return
		}
		if n := updateNotice(cmd.Context()); n != "" {
			fmt.Fprintln(os.Stderr, n)
		}
	}
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	root := Root()
	root.SetContext(ctx)
	return executeRoot(root)
}

// executeRoot runs a prepared command tree and maps its error to an exit code.
func executeRoot(root *cobra.Command) int {
	ctx := root.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitcode.OK
	}
	var coded *exitcode.Coded
	if errors.As(err, &coded) && coded.Msg == "" {
		return coded.Code // the command already reported
	}
	code := api.CodeFor(err)
	if settings.JSON {
		fmt.Fprintf(os.Stdout, "{\n  \"error\": %q,\n  \"exit_code\": %d\n}\n", err.Error(), code)
	} else {
		fmt.Fprintln(os.Stderr, "supercool: "+err.Error())
	}
	return code
}

// silent returns an error that only sets the exit code (already reported).
func silent(code int) error {
	if code == exitcode.OK {
		return nil
	}
	return &exitcode.Coded{Code: code}
}
