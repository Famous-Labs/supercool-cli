package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Famous-Labs/supercool-cli/internal/auth"
	"github.com/Famous-Labs/supercool-cli/internal/download"
	"github.com/Famous-Labs/supercool-cli/internal/exitcode"
)

func workCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "work", Short: "Your recent work: list, get, open"}
	cmd.AddCommand(workListCmd(), workGetCmd(), workOpenCmd())
	return cmd
}

func workListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent work",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			if err := a.requireLogin(); err != nil {
				return err
			}
			items, err := a.client.WorkList(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				a.ui.Info("No work yet. Try: supercool ask \"…\"")
			}
			for _, w := range items {
				title := w.Title
				if title == "" {
					title = "(untitled)"
				}
				a.ui.Print("%s  %s  %s", w.WorkID, title, w.Link)
			}
			a.ui.Result(map[string]any{"work": items})
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "how many")
	return cmd
}

func workGetCmd() *cobra.Command {
	var (
		dl       bool
		out      string
		fromChar int
	)
	cmd := &cobra.Command{
		Use:   "get <work-id>",
		Short: "Show a chat's status, latest result and files (--download saves them)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			if err := a.requireLogin(); err != nil {
				return err
			}
			ctx := cmd.Context()
			if out == "-" {
				if a.s.JSON {
					return exitcode.New(exitcode.Error, "--out - can't be combined with --json")
				}
				a.ui.Out = os.Stderr // stdout carries only the file's bytes
			}
			w, err := a.client.Work(ctx, args[0], fromChar)
			if err != nil {
				return err
			}
			if dl && out == "-" {
				if len(w.Files) != 1 {
					return exitcode.New(exitcode.Error, fmt.Sprintf("--out - takes exactly one file; this chat has %d (use --out DIR)", len(w.Files)))
				}
				return download.Stream(ctx, w.Files[0], os.Stdout)
			}
			a.ui.Info("%s (%s)  %s", w.Title, w.Status, w.Link)
			if w.Text != "" {
				a.ui.Print("%s", w.Text)
			}
			if w.NextFromChar != nil {
				a.ui.Info("More text: supercool work get %s --from-char %d", w.WorkID, *w.NextFromChar)
			}
			var saved []savedFile
			failed := false
			if dl && len(w.Files) > 0 {
				j, err := a.journal()
				if err != nil {
					return err
				}
				dir := out
				if dir == "" {
					dir = filepath.Join("supercool", download.Slug(w.Title, w.WorkID))
				}
				saver := &download.Saver{Client: a.client, Journal: j}
				for _, f := range w.Files {
					p, err := saver.Save(ctx, f, dir)
					if err != nil {
						a.ui.Warn("%v", err)
						failed = true
						continue
					}
					a.ui.Success("Saved %s", p)
					saved = append(saved, savedFile{ID: f.ID, WorkID: f.WorkID, Name: f.FileName, Kind: f.Kind, Path: p})
				}
			} else {
				for _, f := range w.Files {
					a.ui.Print("%s: %s", f.FileName, f.URL)
				}
			}
			code := exitcode.OK
			if failed {
				code = exitcode.DownloadFailed // a file didn't save: never report success
			}
			a.ui.Result(map[string]any{"work": w, "saved": saved, "exit_code": code})
			return silent(code)
		},
	}
	cmd.Flags().BoolVarP(&dl, "download", "d", false, "save the files")
	cmd.Flags().StringVarP(&out, "out", "o", "", "where to save files (default ./supercool/<work-title>/)")
	cmd.Flags().IntVar(&fromChar, "from-char", 0, "read the result text from this character on")
	return cmd
}

func workOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <work-id>",
		Short: "Open the chat in your browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			if err := a.requireLogin(); err != nil {
				return err
			}
			w, err := a.client.Work(cmd.Context(), args[0], 0)
			if err != nil {
				return err
			}
			if auth.OpenBrowser(w.Link) != nil {
				a.ui.Info("Open: %s", w.Link)
			}
			a.ui.Print("%s", w.Link)
			a.ui.Result(map[string]any{"link": w.Link})
			return nil
		},
	}
}
