//go:build !windows

package download

import (
	"os"
	"syscall"
)

// oNoFollow makes open fail on a symlink instead of following it.
const oNoFollow = syscall.O_NOFOLLOW

// openAppend opens an existing partial for appending, refusing a symlink
// atomically (O_NOFOLLOW).
func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|oNoFollow, 0o644)
}
