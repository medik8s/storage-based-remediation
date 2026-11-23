//go:build !linux

package nodestate

import "errors"

// This package targets Linux nodes; see fs_other.go. Without statfs there is
// no way to tell a durable filesystem from a temporary one, so the check
// reports that it could not run rather than guessing.
func filesystemType(string) (int64, error) {
	return 0, errors.New("filesystem type is not reported on this platform")
}

func describeFilesystem(int64) (name, reason string) { return "", "" }
