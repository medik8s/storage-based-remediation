package nodestate

import "fmt"

// Component identifies one writer of node-local state. It is also the base
// name of that writer's file inside the state directory, so it is constrained
// to a conservative character set: a component name can never be a path.
//
// Named constants are conveniences, not an installed-component registry. Any
// controller may define its own component, provided the name validates.
type Component string

const (
	// ComponentSBR is the storage-based-remediation agent.
	ComponentSBR Component = "sbr"
	// ComponentSNR names a potential self-node-remediation consumer.
	// Its presence does not require SNR to use this package or create a file.
	ComponentSNR Component = "snr"
)

// maxComponentLen keeps the derived file name comfortably inside the 255 byte
// limit of every filesystem this runs on, with room for the quarantine suffix.
const maxComponentLen = 32

// Validate reports whether c is usable as a file name inside the state
// directory. The rules are deliberately narrow:
//
//   - 1 to 32 characters;
//   - lower-case ASCII letters, digits and '-' only;
//   - first and last characters are a letter or a digit.
//
// This rejects "", ".", "..", anything containing a path separator or a NUL
// byte, and anything whose case or whitespace could produce two names for one
// file. Validate is the only defence against a caller deriving a component
// from untrusted input, so it refuses rather than sanitises.
func (c Component) Validate() error {
	if len(c) == 0 {
		return fmt.Errorf("%w: must not be empty", ErrInvalidComponent)
	}
	if len(c) > maxComponentLen {
		return fmt.Errorf("%w: %q is longer than %d characters", ErrInvalidComponent, string(c), maxComponentLen)
	}
	for i := 0; i < len(c); i++ {
		ch := c[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
		case ch == '-' && i != 0 && i != len(c)-1:
		default:
			return fmt.Errorf("%w: %q contains %q at offset %d; allowed are [a-z0-9] and '-' between them",
				ErrInvalidComponent, string(c), string(rune(ch)), i)
		}
	}
	return nil
}

// String implements fmt.Stringer.
func (c Component) String() string { return string(c) }

// FileName returns the name of c's state file within the state directory. It
// is only meaningful for a component that passes Validate.
func (c Component) FileName() string { return string(c) + FileExtension }

// KnownComponents returns the built-in component names in stable order.
// It does not describe what is installed or which histories a caller requires.
// ReadAll and Snapshot default to the owning component, not this catalog.
// Pass an explicitly selected set to read multiple components.
func KnownComponents() []Component {
	return []Component{ComponentSBR, ComponentSNR}
}
