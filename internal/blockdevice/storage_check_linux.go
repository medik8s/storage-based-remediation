/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package blockdevice

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// incompatibleFSTypes maps filesystem magic numbers to human-readable names for
// storage backends that are known to be incompatible with block mode. Block
// mode requires O_DIRECT to provide cache-bypass semantics across nodes;
// these filesystem types do not reliably provide that guarantee.
//
// This is a blocklist of known-incompatible types, not a guarantee of
// distributed cache coherence on other filesystem types.
var incompatibleFSTypes = map[int64]string{
	int64(unix.NFS_SUPER_MAGIC):  "NFS",
	int64(unix.CIFS_SUPER_MAGIC): "CIFS/SMB",
	int64(unix.FUSE_SUPER_MAGIC): "FUSE",
}

// blockModeFilesystemUnsupported checks whether the given filesystem type
// (from fstatfs) is on the blocklist of known-incompatible backends. Returns
// the human-readable name and true if incompatible, or ("", false) otherwise.
func blockModeFilesystemUnsupported(fsType int64) (string, bool) {
	name, ok := incompatibleFSTypes[fsType]
	return name, ok
}

type linuxStorageChecker struct{}

// ValidateBlockModeStorage validates the locally observable requirements and
// supported-filesystem policy for block mode. It does not prove that the
// underlying storage backend provides distributed cache coherence.
//
//  1. Verifies O_DIRECT is set on the file descriptor via fcntl(F_GETFL). This
//     is a deterministic check that catches code bugs where the open path
//     accidentally omits O_DIRECT. It does not prove that the underlying storage
//     honors O_DIRECT semantics.
//
//  2. Uses fstatfs(2) to detect the backing filesystem type and rejects
//     known-incompatible types (NFS, CIFS, FUSE). This enforces a support
//     policy — block mode is only supported on storage backends not in the
//     blocklist — rather than proving correctness of any particular backend.
func (linuxStorageChecker) ValidateBlockModeStorage(f *os.File, path string) error {
	// Check 1: verify O_DIRECT is actually set on this fd.
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return fmt.Errorf("fcntl(F_GETFL) failed on %s: %w", path, err)
	}
	if flags&unix.O_DIRECT == 0 {
		return fmt.Errorf("O_DIRECT is not set on %s: block mode requires O_DIRECT "+
			"for cache-bypass reads across nodes", path)
	}

	// Check 2: reject known-incompatible filesystem types.
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &stat); err != nil {
		return fmt.Errorf("fstatfs failed on %s: %w", path, err)
	}

	fsType := int64(stat.Type)
	if name, incompatible := blockModeFilesystemUnsupported(fsType); incompatible {
		return fmt.Errorf("block mode is not supported on %s-backed storage (%s): "+
			"%s does not reliably provide the cache-bypass semantics that block mode "+
			"requires. Use filesystem mode (volumeMode: Filesystem) instead",
			name, path, name)
	}

	return nil
}
