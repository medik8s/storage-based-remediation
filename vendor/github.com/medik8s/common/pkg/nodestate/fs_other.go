//go:build !unix

package nodestate

import "os"

// This package targets Linux nodes. The definitions here exist only so that a
// developer on another platform can still build and vet the tree; the
// guarantees documented elsewhere in the package are weaker here, and
// deploying on such a platform is not supported.

// osNoFollow has no portable equivalent outside Unix. The Lstat check in
// security.go still refuses a symlink, but without the atomicity O_NOFOLLOW
// provides.
const osNoFollow = 0

// osCloexec has no portable equivalent outside Unix.
const osCloexec = 0

// syncDir cannot flush a directory handle on this platform. A rename is still
// atomic with respect to a concurrent reader; it is only durability across a
// power loss that is not guaranteed.
func syncDir(string) error { return nil }

// fileOwner does not report ownership on this platform, so the ownership
// check is skipped and only the permission bits are enforced.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }

// currentUID has no meaning on this platform.
func currentUID() int { return 0 }

// isSymlinkOpenRefusal has no platform-specific form here, because there is
// no O_NOFOLLOW to refuse. The Lstat check is the only symlink defence.
func isSymlinkOpenRefusal(error) bool { return false }
