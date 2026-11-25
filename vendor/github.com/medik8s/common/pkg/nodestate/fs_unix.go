//go:build unix

package nodestate

import (
	"errors"
	"os"
	"syscall"
)

// osNoFollow makes an open fail rather than traverse a final symlink, closing
// the window between the Lstat check and the open itself.
const osNoFollow = syscall.O_NOFOLLOW

// osCloexec keeps a state file descriptor out of any process the agent spawns.
const osCloexec = syscall.O_CLOEXEC

// syncDir flushes a directory's entries so that a completed rename survives a
// power loss. Without it, a crash can leave the directory referring to neither
// the old file nor the new one.
func syncDir(name string) error {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|osCloexec, 0)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// fileOwner returns the numeric owner of fi. The second result is false when
// ownership is not reported, which happens for a filesystem implementation
// that does not populate Sys. Callers skip the ownership check in that case
// and rely on the permission bits alone.
func fileOwner(fi os.FileInfo) (uid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return int(st.Uid), true
}

// currentUID returns the effective user id of this process.
func currentUID() int { return os.Geteuid() }

// isSymlinkOpenRefusal reports whether err is the kernel refusing an open
// because O_NOFOLLOW was set and the final component was a symbolic link.
// Linux reports ELOOP for this; several BSDs report EMLINK.
func isSymlinkOpenRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}
