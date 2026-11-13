//go:build linux

package nodestate

import "syscall"

// Filesystem magic numbers, from the kernel's magic.h. Only the ones that
// mean "this will not survive what the state file has to survive".
const (
	magicTmpfs     = 0x01021994
	magicRamfs     = 0x858458f6
	magicOverlayfs = 0x794c7630
)

// filesystemType returns the kernel's magic number for the filesystem
// holding path.
func filesystemType(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Type), nil
}

// describeFilesystem names a filesystem that cannot hold node state, and
// says why. An empty name means the filesystem is acceptable, or at least
// not one of the known-bad ones.
func describeFilesystem(magic int64) (name, reason string) {
	switch magic {
	case magicTmpfs:
		return "tmpfs", "it lives in memory and is emptied on reboot, so every reboot budget restarts at zero"
	case magicRamfs:
		return "ramfs", "it lives in memory and is emptied on reboot, so every reboot budget restarts at zero"
	case magicOverlayfs:
		return "overlayfs", "this is the container's own writable layer, so the state is discarded when the pod is replaced"
	default:
		return "", ""
	}
}
