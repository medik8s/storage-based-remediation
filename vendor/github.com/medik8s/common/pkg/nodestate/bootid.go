package nodestate

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// BootIDPath is the kernel file holding this boot's identifier. It is a UUID
// the kernel generates once per boot and keeps stable until the next one.
const BootIDPath = "/proc/sys/kernel/random/boot_id"

// maxBootIDFileBytes bounds the read. The real file is 37 bytes; a much
// larger one at this path means something other than procfs is mounted
// there, and parsing it is not worth the memory.
const maxBootIDFileBytes = 4096

// ReadBootID returns this boot's kernel identifier.
//
// It is the primitive behind the start-up classification. Comparing the
// current boot id with the one recorded in the state file separates the two
// cases an agent must never confuse:
//
//   - unchanged: the agent restarted and the node did not. History and timers
//     resume; nothing is charged against a reboot budget. A rolling upgrade
//     must land here.
//   - changed: the node rebooted. Combined with the recorded shutdown state,
//     an orderly reboot is distinguished from a watchdog reset, a panic or a
//     power loss.
//
// This is local evidence about the local kernel. It is not proof that a
// remote request completed. SBR first reads the exact Accepted execution from
// the API, then reads the local boot ID, then conditionally publishes the
// matching observation. A peer validates it before recording BootEnded.
// Node.status.nodeInfo.bootID alone cannot establish that ordering.
//
// Every failure returns ErrBootIDUnavailable, wrapped around the underlying
// cause so a caller can tell a missing procfs from a malformed value.
func ReadBootID() (string, error) {
	return ReadBootIDFrom(BootIDPath)
}

// ReadBootIDFrom reads a boot identifier from an explicit path. Use it in
// tests; production callers want ReadBootID.
func ReadBootIDFrom(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: opening %s: %w", ErrBootIDUnavailable, path, err)
	}
	defer func() { _ = f.Close() }()

	raw, err := io.ReadAll(io.LimitReader(f, maxBootIDFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: reading %s: %w", ErrBootIDUnavailable, path, err)
	}
	if len(raw) > maxBootIDFileBytes {
		return "", fmt.Errorf("%w: %s is larger than %d bytes", ErrBootIDUnavailable, path, maxBootIDFileBytes)
	}

	id := strings.TrimSpace(string(raw))
	if err := validateBootID(id); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrBootIDUnavailable, path, err)
	}
	return id, nil
}

// validateBootID checks the canonical 8-4-4-4-12 lower-case hexadecimal form.
//
// The value is compared against a recorded one to decide whether a node
// rebooted, and that decision gates a reboot budget. Accepting whatever
// happens to be in the file would let a malformed value read as "the node
// rebooted" on every start-up.
func validateBootID(id string) error {
	const canonical = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	if len(id) != len(canonical) {
		return fmt.Errorf("%q is %d characters, want %d", id, len(id), len(canonical))
	}
	for i := 0; i < len(canonical); i++ {
		c := id[i]
		if canonical[i] == '-' {
			if c != '-' {
				return fmt.Errorf("%q has %q where a separator belongs, at offset %d", id, string(rune(c)), i)
			}
			continue
		}
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return fmt.Errorf("%q has %q where a hexadecimal digit belongs, at offset %d", id, string(rune(c)), i)
		}
	}
	return nil
}
