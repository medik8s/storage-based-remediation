package nodestate

import (
	"fmt"
	"os"
)

// SecurityPolicy describes what the library requires of the state directory
// and the files in it.
//
// The threat it addresses is narrow and worth stating plainly: any workload
// privileged enough to write /var/lib/medik8s on a node is already privileged
// on that node. These checks are defence in depth, not a trust boundary. What
// they do buy is that a *less* privileged mistake — a stray world-writable
// directory left by an installer, a symlink planted by an unrelated container
// with a sloppy host mount — cannot quietly become a fencing control surface.
//
// The library reports a policy violation. It never repairs one: an unexpected
// mode means something other than this agent is managing the path, and
// silently correcting it would hide that.
type SecurityPolicy struct {
	// DirMode is the mode the state directory is created with. Default 0700.
	DirMode os.FileMode
	// FileMode is the mode state files are created with. Default 0600.
	FileMode os.FileMode

	// ForbiddenDirModeBits are permission bits the state directory must not
	// have. Default 0077, which is "nothing for group or other".
	ForbiddenDirModeBits os.FileMode
	// ForbiddenFileModeBits are permission bits a state file must not have.
	// Default 0077.
	ForbiddenFileModeBits os.FileMode

	// AllowedUIDs are the numeric owners accepted for the directory and its
	// files. When nil, root and this process's effective user are accepted,
	// which covers both a production agent running as root and a unit test
	// running as anyone.
	AllowedUIDs []int

	// AllowSymlinks disables both symlink refusals: the state directory and
	// the files in it may be links, and the directory's path may reach it
	// through one.
	//
	// It exists for the rare deployment that genuinely redirects the state
	// directory, and for tests on a platform whose temporary directories sit
	// under a link — macOS puts them under /var, which resolves to
	// /private/var. Setting it gives up the guarantee that a write cannot be
	// aimed at another file.
	AllowSymlinks bool
}

// DefaultSecurityPolicy returns the policy the medik8s contract specifies: a
// 0700 directory and 0600 files, owned by root or by this process, with no
// symlinks.
func DefaultSecurityPolicy() SecurityPolicy {
	return SecurityPolicy{
		DirMode:               0o700,
		FileMode:              0o600,
		ForbiddenDirModeBits:  0o077,
		ForbiddenFileModeBits: 0o077,
	}
}

// Validate reports whether the policy is self-consistent.
func (p SecurityPolicy) Validate() error {
	if p.DirMode.Perm() == 0 {
		return fmt.Errorf("security policy: DirMode must grant the owner access, got %#o", p.DirMode)
	}
	if p.FileMode.Perm() == 0 {
		return fmt.Errorf("security policy: FileMode must grant the owner access, got %#o", p.FileMode)
	}
	if p.DirMode.Perm()&p.ForbiddenDirModeBits != 0 {
		return fmt.Errorf("security policy: DirMode %#o sets bits ForbiddenDirModeBits %#o rejects",
			p.DirMode.Perm(), p.ForbiddenDirModeBits)
	}
	if p.FileMode.Perm()&p.ForbiddenFileModeBits != 0 {
		return fmt.Errorf("security policy: FileMode %#o sets bits ForbiddenFileModeBits %#o rejects",
			p.FileMode.Perm(), p.ForbiddenFileModeBits)
	}
	for _, uid := range p.AllowedUIDs {
		if uid < 0 {
			return fmt.Errorf("security policy: AllowedUIDs contains %d", uid)
		}
	}
	return nil
}

// allowsUID reports whether uid may own the state directory or its files.
func (p SecurityPolicy) allowsUID(uid int) bool {
	if len(p.AllowedUIDs) == 0 {
		return uid == 0 || uid == currentUID()
	}
	for _, allowed := range p.AllowedUIDs {
		if uid == allowed {
			return true
		}
	}
	return false
}

// checkOwnership verifies the owner of fi against the policy. When the
// filesystem does not report an owner the check passes: see fileOwner.
func (p SecurityPolicy) checkOwnership(path string, fi os.FileInfo) error {
	uid, ok := fileOwner(fi)
	if !ok {
		return nil
	}
	if !p.allowsUID(uid) {
		return pathErrorf("verify", path, fmt.Errorf("%w: owned by uid %d", ErrInsecureOwner, uid))
	}
	return nil
}

// checkDirectory verifies that fi describes a directory this library may use.
// fi must come from an Lstat, so that a symlink is still visible as one.
func (p SecurityPolicy) checkDirectory(path string, fi os.FileInfo) error {
	if fi.Mode()&os.ModeSymlink != 0 && !p.AllowSymlinks {
		return pathErrorf("verify", path, fmt.Errorf("%w: the state directory must not be a symbolic link", ErrSymlink))
	}
	if !fi.IsDir() {
		return pathErrorf("verify", path, fmt.Errorf("%w: mode %s", ErrNotDirectory, fi.Mode()))
	}
	if bad := fi.Mode().Perm() & p.ForbiddenDirModeBits; bad != 0 {
		return pathErrorf("verify", path, fmt.Errorf("%w: mode %#o sets %#o, which other users can act on",
			ErrInsecurePermissions, fi.Mode().Perm(), bad))
	}
	return p.checkOwnership(path, fi)
}

// checkFile verifies that fi describes a state file this library may trust.
//
// Called twice per read: once on the Lstat result, to decide whether to open
// at all, and once on the open descriptor, because everything learned from a
// path can be invalidated before the open completes. The second call is the
// one that matters; the first only avoids opening something that is obviously
// wrong.
func (p SecurityPolicy) checkFile(path string, fi os.FileInfo, fromLstat bool) error {
	if fromLstat && fi.Mode()&os.ModeSymlink != 0 && !p.AllowSymlinks {
		return pathErrorf("verify", path, fmt.Errorf("%w: refusing to read through it", ErrSymlink))
	}
	if !fi.Mode().IsRegular() {
		return pathErrorf("verify", path, fmt.Errorf("%w: mode %s", ErrNotRegular, fi.Mode()))
	}
	if bad := fi.Mode().Perm() & p.ForbiddenFileModeBits; bad != 0 {
		return pathErrorf("verify", path, fmt.Errorf("%w: mode %#o sets %#o, so the contents cannot be trusted",
			ErrInsecurePermissions, fi.Mode().Perm(), bad))
	}
	return p.checkOwnership(path, fi)
}
