package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Famous-Labs/supercool-cli/internal/auth"
	"github.com/Famous-Labs/supercool-cli/internal/version"
)

func loginCmd() *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to SuperCool (opens your browser)",
		Long: `Sign in to SuperCool. Opens your browser once and remembers the login.

On an SSH box or a machine without a browser, use --no-browser: open the link
on any device, sign in, and paste back the code it shows.

For CI and scripts, create a personal access token instead (supercool token)
and set SUPERCOOL_TOKEN.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			cfg, err := a.client.CLIConfig(ctx)
			if err != nil {
				return fmt.Errorf("couldn't reach SuperCool at %s: %w", a.s.APIURL, err)
			}
			var creds *auth.Credentials
			if noBrowser {
				creds, err = auth.LoginPaste(ctx, cfg, a.s.APIURL, os.Stdin, os.Stderr)
			} else {
				creds, err = auth.LoginBrowser(ctx, cfg, a.s.APIURL, os.Stderr, false)
			}
			if err != nil {
				return err
			}
			key := auth.Key(a.s.Profile, a.s.APIURL)
			if old, _ := auth.Load(key); old != nil {
				auth.Revoke(ctx, old) // replacing a login: drop the old one server-side
			}
			if err := auth.Save(key, creds); err != nil {
				return fmt.Errorf("signed in, but couldn't save the login: %w", err)
			}
			a2, err := newApp()
			if err != nil {
				return err
			}
			me, err := a2.client.Me(ctx)
			if err != nil {
				a.ui.Success("Signed in.")
				return nil
			}
			a.ui.Success("Signed in as %s. Your agent %s is ready.", me.Email, me.Agent.Name)
			a.ui.Result(map[string]any{"signed_in": true, "email": me.Email, "agent": me.Agent.Name})
			return nil
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "sign in from another device and paste back a code (SSH, headless)")
	return cmd
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Sign out and forget the saved login",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			key := auth.Key(a.s.Profile, a.s.APIURL)
			c, _ := auth.Load(key)
			auth.Revoke(cmd.Context(), c)
			if err := auth.Delete(key); err != nil {
				return err
			}
			a.ui.Success("Signed out.")
			a.ui.Result(map[string]any{"signed_out": true})
			return nil
		},
	}
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in account, plan and credits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			if err := a.requireLogin(); err != nil {
				return err
			}
			me, err := a.client.Me(cmd.Context())
			if err != nil {
				return err
			}
			credits := "unknown"
			if me.Credits != nil {
				credits = fmt.Sprintf("%d", *me.Credits)
			}
			plan := me.Plan
			if plan == "" {
				plan = "free"
			}
			a.ui.Print("%s — %s plan, %s credits, agent %s", me.Email, plan, credits, me.Agent.Name)
			a.ui.Result(me)
			return nil
		},
	}
}

func tokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Create or revoke personal access tokens (opens the dashboard)",
		Long: `Personal access tokens let CI and scripts use your agent without a browser:

  SUPERCOOL_TOKEN=sc_key_… supercool ask "…" --wait

For safety, tokens are created and revoked only in the SuperCool dashboard,
from a signed-in browser. This opens that page.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			url := "https://supercool.com/dashboard#api"
			if cfg, err := a.client.CLIConfig(cmd.Context()); err == nil && cfg.DashboardTokensURL != "" {
				url = cfg.DashboardTokensURL
			}
			if auth.OpenBrowser(url) != nil {
				a.ui.Info("Open this page to manage tokens:")
			} else {
				a.ui.Info("Opened the token page in your browser:")
			}
			a.ui.Print("%s", url)
			a.ui.Result(map[string]any{"url": url})
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			settings.Resolve()
			if settings.JSON {
				fmt.Printf("{\"version\": %q}\n", version.Version)
				return
			}
			fmt.Println(version.String())
		},
	}
}
