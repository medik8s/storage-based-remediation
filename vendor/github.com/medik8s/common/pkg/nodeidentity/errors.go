package nodeidentity

import "errors"

// Sentinel errors. All errors returned by this package wrap one of these, so
// callers can branch with errors.Is. Retryable: ErrConflict (re-GET, re-validate,
// re-patch with bounded jittered backoff). Everything else is terminal for the
// current attempt.
var (
	// ErrInvalidConfig is returned for a Config with missing or malformed fields.
	ErrInvalidConfig = errors.New("nodeidentity: invalid configuration")

	// ErrCertUnavailable is returned when the client certificate or key cannot
	// be read or parsed from disk.
	ErrCertUnavailable = errors.New("nodeidentity: client certificate unavailable")

	// ErrCertExpired is returned when the client certificate's validity window
	// has passed. Callers should report expiry; it is not remediable while the
	// node is held.
	ErrCertExpired = errors.New("nodeidentity: client certificate expired")

	// ErrCertNotYetValid is returned when the client certificate's validity
	// window has not started.
	ErrCertNotYetValid = errors.New("nodeidentity: client certificate not yet valid")

	// ErrCertKeyMismatch is returned when the certificate and key parse but do
	// not belong together.
	ErrCertKeyMismatch = errors.New("nodeidentity: client certificate and key do not match")

	// ErrCAUnavailable is returned when the CA bundle cannot be read or parsed.
	ErrCAUnavailable = errors.New("nodeidentity: CA bundle unavailable")

	// ErrUnauthorized is returned for HTTP 401 responses.
	ErrUnauthorized = errors.New("nodeidentity: api server rejected the credentials")

	// ErrForbidden is returned for HTTP 403 responses. For taint writes this is
	// the expected NodeRestriction refusal.
	ErrForbidden = errors.New("nodeidentity: node identity is not permitted for this request")

	// ErrNodeNotFound is returned for HTTP 404 responses.
	ErrNodeNotFound = errors.New("nodeidentity: node not found")

	// ErrNodeNameMismatch is returned when the response or live Node does not
	// match the configured node name or the expected Node UID. The Node object
	// may have been replaced; do not act on it.
	ErrNodeNameMismatch = errors.New("nodeidentity: node identity mismatch")

	// ErrConflict is returned for HTTP 409 responses (stale resourceVersion
	// precondition). Retryable by the caller: re-GET, re-validate, re-patch.
	ErrConflict = errors.New("nodeidentity: resourceVersion precondition failed")

	// ErrAPIServer is returned for transport failures, TLS failures, redirects,
	// unexpected statuses, and undecodable responses.
	ErrAPIServer = errors.New("nodeidentity: api server error")
)
