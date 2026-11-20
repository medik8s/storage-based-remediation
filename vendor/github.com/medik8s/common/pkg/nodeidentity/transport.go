package nodeidentity

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// NewTransport builds the authenticated transport: HTTP/1.1 only (the custom
// tls.Config suppresses Go's HTTP/2 upgrade), keep-alives disabled, no proxy
// (the host service must not silently route through HTTP_PROXY), and the
// client certificate presented through a per-handshake callback that re-reads
// it from disk. Server verification uses the CA bundle from cfg.CAFile and is
// never optional. See doc.go for the rationale.
func NewTransport(cfg Config) (*http.Transport, error) {
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read %s: %v", ErrCAUnavailable, cfg.CAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: no certificates parsed from %s", ErrCAUnavailable, cfg.CAFile)
	}
	provider := &certProvider{certFile: cfg.CertFile, keyFile: cfg.KeyFile, now: cfg.now}
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:              pool,
			GetClientCertificate: provider.getClientCertificate,
			MinVersion:           tls.VersionTLS12,
		},
		DisableKeepAlives:   true,
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}, nil
}
