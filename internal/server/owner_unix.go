//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"syscall"
)

// matchDirOwner gives a file written by root (e.g. via the CLI) to the owner
// of its directory, so the server running as an unprivileged user can still
// update it.
func matchDirOwner(path string) {
	if os.Geteuid() != 0 {
		return
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
		os.Chown(path, int(st.Uid), int(st.Gid))
	}
}
