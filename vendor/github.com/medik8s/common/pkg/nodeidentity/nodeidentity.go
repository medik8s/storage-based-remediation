package nodeidentity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config is fully explicit: every value comes from a pre-recorded identity
// descriptor resolved by a consumer running while the node was healthy (for
// example an agent's enrollment routine). This package performs no discovery.
type Config struct {
	// NodeName is the name of the Node this identity belongs to. Required.
	NodeName string
	// ExpectedUID is the Node UID recorded at enrollment. The live UID must
	// match. High-level writes carry an atomic UID precondition. Raw
	// PatchOwnNodeStatus callers must supply their own UID guard; response
	// validation alone cannot prevent a write to a replacement Node. Required.
	ExpectedUID string
	// APIServer is the DIRECT host-routable API server endpoint, for example
	// "https://api.cluster.example:6443". It must be reachable without CNI and
	// must not be kubelet's local proxy endpoint (whose static pod does not
	// start when kubelet does not). Required; https only.
	APIServer string
	// CAFile is the CA bundle used to verify the API server, for example
	// "/etc/kubernetes/kubelet-ca.crt". Required. Verification is never optional.
	CAFile string
	// CertFile is the client certificate PEM path — either the kubelet pki
	// symlink or a file materialized by the enrollment routine. Required.
	CertFile string
	// KeyFile is the client key PEM path. Required.
	KeyFile string
	// Now is the clock used for certificate validity checks. Optional (tests).
	Now func() time.Time
	// HTTPClient overrides the built transport entirely. Optional (tests).
	// When set, the client is used as-is and no transport is constructed.
	HTTPClient *http.Client
}

// now returns the configured clock, defaulting to time.Now.
func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// New validates the configuration and returns a Client. It does not touch the
// filesystem: a missing certificate surfaces on first use, not at construction.
func New(cfg Config) (*Client, error) {
	var missing []string
	if strings.TrimSpace(cfg.NodeName) == "" {
		missing = append(missing, "NodeName")
	}
	if strings.TrimSpace(cfg.ExpectedUID) == "" {
		missing = append(missing, "ExpectedUID")
	}
	if strings.TrimSpace(cfg.APIServer) == "" {
		missing = append(missing, "APIServer")
	}
	if strings.TrimSpace(cfg.CAFile) == "" {
		missing = append(missing, "CAFile")
	}
	if strings.TrimSpace(cfg.CertFile) == "" {
		missing = append(missing, "CertFile")
	}
	if strings.TrimSpace(cfg.KeyFile) == "" {
		missing = append(missing, "KeyFile")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: missing required fields: %s", ErrInvalidConfig, strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(cfg.APIServer, "https://") {
		return nil, fmt.Errorf("%w: APIServer must be an https URL, got %q", ErrInvalidConfig, cfg.APIServer)
	}
	cfg.APIServer = strings.TrimSuffix(cfg.APIServer, "/")
	return &Client{cfg: cfg, httpClient: cfg.HTTPClient}, nil
}

// Client is an authenticated own-node API client. It is safe for concurrent
// use. The zero value is not usable; construct it with New.
type Client struct {
	cfg        Config
	httpClient *http.Client

	mu         sync.Mutex // guards transport construction and observedRV
	transport  *http.Transport
	observedRV string
}

// Config returns the configuration the client was constructed with.
func (c *Client) Config() Config { return c.cfg }

// ensureTransport lazily builds the authenticated transport. When Config
// provided an HTTPClient, it is used as-is.
func (c *Client) ensureTransport() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.httpClient != nil {
		return nil
	}
	tr, err := NewTransport(c.cfg)
	if err != nil {
		return err
	}
	c.transport = tr
	c.httpClient = &http.Client{
		Transport: tr,
		// A node-status client has no business following redirects to other
		// hosts; a 3xx is surfaced as an error instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		// A caller using a context without a deadline must not be able to
		// wait indefinitely for a response or a body read. A context with a
		// tighter deadline still wins; this is the bound for everything else.
		Timeout: 30 * time.Second,
	}
	return nil
}

func (c *Client) getObservedRV() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observedRV
}

func (c *Client) setObservedRV(rv string) {
	if rv == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observedRV = rv
}

// clearObservedRV invalidates the cached resourceVersion so the next
// PatchOwnNodeCondition performs a fresh GetOwnNode. Called on conflict and
// identity errors, where the cached precondition is known-stale.
func (c *Client) clearObservedRV() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observedRV = ""
}

// certSentinels are the typed certificate errors that must survive unwrapping
// through the HTTP stack when the per-handshake callback fails.
var certSentinels = []error{ErrCertUnavailable, ErrCertExpired, ErrCertNotYetValid, ErrCertKeyMismatch}

// do performs one HTTP request against the API server. It fails fast with a
// typed certificate error before any network I/O, so an expired or broken
// certificate never depends on server behavior to be noticed.
func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	if err := c.ensureTransport(); err != nil {
		return nil, err
	}
	if _, err := loadClientCertificate(c.cfg.CertFile, c.cfg.KeyFile, c.cfg.now()); err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIServer+path, reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "medik8s-nodeidentity/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		for _, sentinel := range certSentinels {
			if errors.Is(err, sentinel) {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%w: %w", ErrAPIServer, err)
	}
	return resp, nil
}

// drainAndClose discards a bounded remainder of the body and closes it, so
// responses are always released.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxNodeBodyBytes))
	_ = resp.Body.Close()
}
