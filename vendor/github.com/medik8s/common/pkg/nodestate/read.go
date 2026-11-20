package nodestate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
)

// --- reading ---

// readDocument reads and validates one state file. Every failure it can
// return is classifiable with errors.Is: fs.ErrNotExist, ErrSymlink,
// ErrNotRegular, ErrInsecurePermissions, ErrInsecureOwner, ErrTooLarge,
// ErrCorrupt or ErrSchemaUnsupported.
func (s *Store) readDocument(path string, owner Component) (*Document, error) {
	if err := s.verifyDirectoryIfPresent(); err != nil {
		return nil, err
	}
	stat := s.fs.Lstat
	fromLstat := true
	if s.security.AllowSymlinks {
		stat, fromLstat = s.fs.Stat, false
	}

	fi, err := stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err // caller distinguishes absence from damage
		}
		return nil, pathErrorf("stat", path, err)
	}
	if err := s.security.checkFile(path, fi, fromLstat); err != nil {
		return nil, err
	}
	if fi.Size() > int64(s.maxReadBytes) {
		return nil, pathErrorf("read", path,
			fmt.Errorf("%w: %d bytes, over the %d byte read limit", ErrTooLarge, fi.Size(), s.maxReadBytes))
	}

	f, err := s.fs.Open(path, OpenOptions{FollowSymlinks: s.security.AllowSymlinks})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		// A refusal to open is most often O_NOFOLLOW rejecting a link that
		// appeared between the stat and the open. Report it as such rather
		// than as a generic I/O error: it is the TOCTOU case the flag exists
		// for, and a caller seeing it should look at who else writes here.
		if isSymlinkOpenRefusal(err) {
			return nil, pathErrorf("open", path, fmt.Errorf("%w: it was replaced by a link after it was checked", ErrSymlink))
		}
		return nil, pathErrorf("open", path, err)
	}
	defer func() { _ = f.Close() }()

	// Re-check on the descriptor. Everything learned from the path above
	// describes whatever was there a moment ago; this describes what is
	// actually open.
	opened, err := f.Stat()
	if err != nil {
		return nil, pathErrorf("fstat", path, err)
	}
	if err := s.security.checkFile(path, opened, false); err != nil {
		return nil, err
	}

	// The size check above used what stat reported; this one uses what was
	// actually there. A file that grows in between is refused, not truncated.
	data, err := readAtMost(f, s.maxReadBytes)
	if err != nil {
		return nil, pathErrorf("read", path, err)
	}

	return parseDocument(path, data, owner)
}

// readAtMost reads up to limit bytes and reports ErrTooLarge if there are
// more, rather than silently truncating or allocating without bound.
func readAtMost(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%w: more than the %d byte read limit", ErrTooLarge, limit)
	}
	return data, nil
}

func parseDocument(path string, data []byte, owner Component) (*Document, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, pathErrorf("parse", path, fmt.Errorf("%w: the file is empty", ErrCorrupt))
	}

	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, pathErrorf("parse", path, fmt.Errorf("%w: %v", ErrCorrupt, err))
	}

	if doc.SchemaVersion < MinSupportedSchema || doc.SchemaVersion > Schema {
		return nil, pathErrorf("parse", path, &SchemaVersionError{
			Found: doc.SchemaVersion, Min: MinSupportedSchema, Max: Schema,
		})
	}

	// A file whose contents claim a different owner is not this component's
	// state, whatever the name says. An older writer that stamped nothing is
	// accepted: the name is then the only claim available.
	if doc.Agent.Component != "" && doc.Agent.Component != owner {
		return nil, pathErrorf("parse", path, fmt.Errorf(
			"%w: %s contains a document owned by %q", ErrCorrupt, filepath.Base(path), doc.Agent.Component))
	}

	return &doc, nil
}
