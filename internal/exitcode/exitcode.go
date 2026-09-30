// Package exitcode defines the CLI's exit codes. A command that follows
// several pieces of work exits with the most severe outcome among them.
package exitcode

const (
	OK             = 0  // every piece of work completed and its files were saved
	Error          = 1  // anything else: bad input, network, server error
	LoginNeeded    = 2  // not logged in, or the login was revoked or expired
	Credits        = 3  // out of credits: refused, or work stopped partway
	TimedOut       = 4  // --timeout reached; the work is still watched, `wait` rejoins
	Failed         = 5  // the message or a piece of work failed
	Stopped        = 6  // someone stopped the work
	RateLimited    = 7  // rate limited, or the agent was busy
	DownloadFailed = 8  // finished, but a file couldn't be saved (`wait` retries)
	Expired        = 9  // the work outlived the server's watch window; `wait` recovers it
	Unknown        = 10 // recovery found no outcome for this message
)

// severity orders codes from most to least severe.
var severity = []int{LoginNeeded, Failed, Stopped, Credits, Unknown, Expired, RateLimited, TimedOut, DownloadFailed, Error, OK}

func rank(code int) int {
	for i, c := range severity {
		if c == code {
			return i
		}
	}
	return len(severity)
}

// Worst returns the most severe of the given codes (OK when empty).
func Worst(codes ...int) int {
	best := OK
	for _, c := range codes {
		if rank(c) < rank(best) {
			best = c
		}
	}
	return best
}

// ForOutcome maps a server outcome to an exit code.
func ForOutcome(outcome string) int {
	switch outcome {
	case "completed":
		return OK
	case "stalled_credits":
		return Credits
	case "failed":
		return Failed
	case "stopped":
		return Stopped
	case "expired":
		return Expired
	case "unknown":
		return Unknown
	case "busy":
		return RateLimited
	}
	return Error
}

// Coded is an error carrying the exit code to use.
type Coded struct {
	Code int
	Msg  string
}

func (e *Coded) Error() string { return e.Msg }

// New returns a Coded error.
func New(code int, msg string) error { return &Coded{Code: code, Msg: msg} }
