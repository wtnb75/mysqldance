package source

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// SSL modes accepted by Config.SSLMode; they mirror the mysql client's
// --ssl-mode values. The empty string means SSLPreferred.
const (
	SSLDisabled       = "disabled"
	SSLPreferred      = "preferred"
	SSLRequired       = "required"
	SSLVerifyCA       = "verify-ca"
	SSLVerifyIdentity = "verify-identity"
)

// ValidateSSL checks SSLMode and SSLCA for consistency without touching the
// file system or the network, so callers can reject bad flags up front.
func (c Config) ValidateSSL() error {
	switch c.SSLMode {
	case "", SSLDisabled, SSLPreferred:
		if c.SSLCA != "" {
			return fmt.Errorf("ssl-ca needs ssl-mode required, verify-ca or verify-identity, not %q", c.SSLMode)
		}
	case SSLVerifyCA:
		// Without a CA file the system roots would accept any publicly
		// issued certificate for any host, so the mysql client requires one too.
		if c.SSLCA == "" {
			return errors.New("ssl-mode verify-ca needs ssl-ca")
		}
	case SSLRequired:
	case SSLVerifyIdentity:
		// There is no host name to check on a Unix socket.
		if c.Socket != "" {
			return errors.New("ssl-mode verify-identity cannot be used with a socket; use verify-ca")
		}
	default:
		return fmt.Errorf("unknown ssl-mode %q: want disabled, preferred, required, verify-ca or verify-identity", c.SSLMode)
	}
	return nil
}

// tlsConfig builds the client TLS settings for cfg. host is the name the
// server certificate is checked against in verify-identity mode. It returns a
// nil config when the connection must stay in clear text. fallback reports
// whether the driver may fall back to clear text when the server has no TLS;
// only preferred allows it, so every stricter mode fails instead of sending
// credentials and rows unencrypted.
func tlsConfig(cfg Config, host string) (c *tls.Config, fallback bool, err error) {
	if err := cfg.ValidateSSL(); err != nil {
		return nil, false, err
	}
	mode := cfg.SSLMode
	if mode == "" {
		mode = SSLPreferred
	}
	// Like the mysql client, do not use TLS on a Unix socket unless asked to.
	if mode == SSLDisabled || (mode == SSLPreferred && cfg.Socket != "") {
		return nil, false, nil
	}

	var roots *x509.CertPool // nil means the system roots
	if cfg.SSLCA != "" {
		pem, err := os.ReadFile(cfg.SSLCA)
		if err != nil {
			return nil, false, fmt.Errorf("read ssl-ca: %w", err)
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, false, fmt.Errorf("ssl-ca %s: no certificates found", cfg.SSLCA)
		}
	}

	// InsecureSkipVerify below is deliberate: preferred and required encrypt
	// without verifying (as the mysql client does), and verify-ca replaces
	// the built-in check with verifyChain. Only verify-identity uses the
	// standard verification.
	switch {
	case mode == SSLPreferred:
		return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, true, nil
	case mode == SSLVerifyIdentity:
		return &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}, false, nil
	case mode == SSLVerifyCA || (mode == SSLRequired && roots != nil):
		// verify-ca (or required with a CA file, by design: a CA that was
		// given must not be ignored) checks the chain but not the host
		// name. crypto/tls cannot do that natively, so skip its
		// verification and run the chain check ourselves.
		return &tls.Config{
			InsecureSkipVerify:    true,
			VerifyPeerCertificate: verifyChain(roots),
			MinVersion:            tls.VersionTLS12,
		}, false, nil
	default: // required without a CA file: encrypt only
		return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, false, nil
	}
}

// verifyChain returns a tls.Config.VerifyPeerCertificate callback that checks
// the server's certificate chain against roots without checking its name.
func verifyChain(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("server presented no certificate")
		}
		certs := make([]*x509.Certificate, len(rawCerts))
		for i, raw := range rawCerts {
			c, err := x509.ParseCertificate(raw)
			if err != nil {
				return fmt.Errorf("parse server certificate: %w", err)
			}
			certs[i] = c
		}
		inter := x509.NewCertPool()
		for _, c := range certs[1:] {
			inter.AddCert(c)
		}
		_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
		return err
	}
}
