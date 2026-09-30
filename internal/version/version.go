// Package version holds build information, set by GoReleaser via -ldflags.
package version

import (
	"fmt"
	"runtime"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// UserAgent identifies the CLI to the API (server-side usage stats only).
func UserAgent() string {
	return fmt.Sprintf("supercool-cli/%s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH)
}

// String is what `supercool version` prints.
func String() string {
	return fmt.Sprintf("supercool %s (%s) built %s", Version, Commit, Date)
}
