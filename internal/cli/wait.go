package cli

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Famous-Labs/supercool-cli/internal/api"
	"github.com/Famous-Labs/supercool-cli/internal/exitcode"
	"github.com/Famous-Labs/supercool-cli/internal/journal"
)

func waitCmd() *cobra.Command {
	var (
		out     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait [request-id]",
		Short: "Rejoin work started earlier and save its files",
		Long: `Rejoin a message's work: after a timeout, a lost connection, or from another
machine. Saves any files not saved yet.

If the work outlived the server's watch window, wait recovers it from where it
stopped. It never sends the message again, so nothing is started or billed twice.

With no request id, waits for everything still outstanding on this machine.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			j, err := a.journal()
			if err != nil {
				return err
			}
			var reqs []*journal.Request
			if len(args) == 1 {
				req, err := j.LoadRequest(args[0])
				if err != nil {
					return err
				}
				if req == nil { // started elsewhere: the server knows where it began
					req = &journal.Request{ID: args[0], CreatedAt: time.Now(), Sent: true,
						Work: map[string]string{}, Outcomes: map[string]string{}}
				}
				reqs = []*journal.Request{req}
			} else if reqs, err = j.Unfinished(); err != nil {
				return err
			}
			if len(reqs) == 0 {
				a.ui.Info("Nothing outstanding.")
				a.ui.Result(map[string]any{"requests": []any{}, "exit_code": 0})
				return nil
			}
			codes := []int{}
			for _, req := range reqs {
				if out != "" {
					req.Out = out
				}
				if req.Out == "-" {
					if a.s.JSON {
						return exitcode.New(exitcode.Error, "--out - can't be combined with --json")
					}
					a.ui.Out = os.Stderr
				}
				codes = append(codes, rejoin(cmd.Context(), a, j, req, timeout))
			}
			return silent(exitcode.Worst(codes...))
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "where to save files (default ./supercool/<work-title>/)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long (the work keeps going)")
	return cmd
}

// rejoin resumes one request: resend if it never went out, recover expired
// watches, then follow it. Returns its exit code (already reported).
func rejoin(ctx context.Context, a *app, j *journal.Journal, req *journal.Request, timeout time.Duration) int {
	if !req.Sent {
		if req.Text == "" && len(req.FilePaths) == 0 && len(req.UploadIDs) == 0 {
			a.ui.Warn("%s was never sent and there's nothing to resend.", req.ID)
			return exitcode.Error
		}
		if _, err := send(ctx, a, j, req, true); err != nil {
			a.ui.Warn("%v", err)
			return codeOf(err)
		}
	}
	// Always ask the server's recovery records, not just when this machine
	// saw an expiry: after days without polling, the watch can be gone with
	// no expiry ever recorded here. Recovery never resends the message; for
	// work still being watched it changes nothing.
	{
		a.ui.Status("Checking on the work…")
		rec, err := a.client.Recover(ctx, req.ID)
		if err != nil {
			a.ui.Warn("%v", err)
			return codeOf(err)
		}
		for _, w := range rec.Work {
			switch w.State {
			case "settled":
				req.Outcomes[w.WorkID] = w.Outcome
			case "recovering", "watching":
				delete(req.Outcomes, w.WorkID)
			case "exhausted":
				a.ui.Warn("%s can't be recovered again; open it with: supercool work open %s", titleOf(req, w.WorkID), w.WorkID)
			}
		}
		req.Recovered++
		_ = j.SaveRequest(req)
	}
	if err := follow(ctx, a, j, req, nil, timeout); err != nil {
		return codeOf(err)
	}
	return exitcode.OK
}

func codeOf(err error) int { return api.CodeFor(err) }
