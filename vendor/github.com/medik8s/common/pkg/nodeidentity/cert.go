package nodeidentity

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// loadClientCertificate reads and validates the client certificate and key
// pair from disk. It is called before every request (so certificate problems
// surface as typed errors before any network I/O) and from the per-handshake
// TLS callback (see NewTransport). Keep-alives are disabled, so the callback
// runs on every request and always presents the certificate currently on disk.
func loadClientCertificate(certFile, keyFile string, now time.Time) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read %s: %v", ErrCertUnavailable, certFile, err)
	}
	// The common deployment points both paths at the same combined PEM
	// (kubelet's client file); read it once instead of twice per handshake.
	keyPEM := certPEM
	if keyFile != certFile {
		keyPEM, err = os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot read %s: %v", ErrCertUnavailable, keyFile, err)
		}
	}
	return parseClientPEM(certPEM, keyPEM, now)
}

// parseClientPEM validates a certificate/key PEM pair and returns the parsed
// tls.Certificate. The pair must be individually parseable and belong together;
// the leaf's validity window is checked against now.
//
// Both inputs may be COMBINED files: kubelet's client material on OpenShift
// is a single PEM with the certificate followed by the key, and Config
// commonly points CertFile and KeyFile at the same path. The loaders below
// scan for the first block of the required type instead of assuming it is
// the first block in the file.
func parseClientPEM(certPEM, keyPEM []byte, now time.Time) (*tls.Certificate, error) {
	certBlock := firstPEMBlock(certPEM, "CERTIFICATE")
	if certBlock == nil {
		return nil, fmt.Errorf("%w: no CERTIFICATE PEM block found", ErrCertUnavailable)
	}
	if _, err := x509.ParseCertificate(certBlock.Bytes); err != nil {
		return nil, fmt.Errorf("%w: certificate block does not parse: %v", ErrCertUnavailable, err)
	}

	keyBlock := firstPrivateKeyPEMBlock(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("%w: no private key PEM block found", ErrCertUnavailable)
	}
	if err := parsePrivateKeyPEM(keyBlock); err != nil {
		return nil, fmt.Errorf("%w: private key block does not parse: %v", ErrCertUnavailable, err)
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// Both blocks parse individually but the pair is unusable: the key does
		// not belong to this certificate.
		return nil, fmt.Errorf("%w: %v", ErrCertKeyMismatch, err)
	}
	if pair.Leaf == nil {
		leaf, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCertUnavailable, err)
		}
		pair.Leaf = leaf
	}

	if now.Before(pair.Leaf.NotBefore) {
		return nil, fmt.Errorf("%w: not valid before %s",
			ErrCertNotYetValid, pair.Leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	if now.After(pair.Leaf.NotAfter) {
		return nil, fmt.Errorf("%w: expired %s",
			ErrCertExpired, pair.Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	return &pair, nil
}

// parsePrivateKeyPEM parses a PEM private key block (PKCS#1, SEC 1, or PKCS#8)
// to validate it in isolation from the certificate.
func parsePrivateKeyPEM(block *pem.Block) error {
	var key crypto.PrivateKey
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return fmt.Errorf("unsupported private key PEM type %q", block.Type)
	}
	if err != nil {
		return err
	}
	switch key.(type) {
	case *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey:
		return nil
	default:
		return fmt.Errorf("unsupported private key algorithm %T", key)
	}
}

// privateKeyPEMTypes are the PEM block types parsePrivateKeyPEM accepts.
var privateKeyPEMTypes = map[string]bool{
	"RSA PRIVATE KEY": true,
	"EC PRIVATE KEY":  true,
	"PRIVATE KEY":     true,
}

// firstPEMBlock returns the first PEM block of exactly the given type, or
// nil. A combined file (certificate followed by key, as kubelet writes) is
// handled by scanning rather than assuming the first block matches.
func firstPEMBlock(data []byte, blockType string) *pem.Block {
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil
		}
		if block.Type == blockType {
			return block
		}
	}
}

// firstPrivateKeyPEMBlock returns the first PEM block that is a private key
// type, skipping certificates and other blocks that precede it in a combined
// file.
func firstPrivateKeyPEMBlock(data []byte) *pem.Block {
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil
		}
		if privateKeyPEMTypes[block.Type] {
			return block
		}
	}
}

// certProvider implements the per-handshake certificate callback. It is
// stateless on purpose: every handshake re-reads the pair from disk, so the
// presented certificate is always the current one. There is no watcher, no
// cache, and no rotation logic — see doc.go for why that is sufficient.
type certProvider struct {
	certFile string
	keyFile  string
	now      func() time.Time
}

func (p *certProvider) getClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return loadClientCertificate(p.certFile, p.keyFile, p.now())
}
