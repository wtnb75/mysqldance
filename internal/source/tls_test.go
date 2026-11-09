package source

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// testCert creates a certificate for name signed by parent (self-signed CA
// when parent is nil) and returns it with its key.
func testCert(t *testing.T, name string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		DNSNames:              []string{name},
	}
	if isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, der
}

func writeCA(t *testing.T, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTLSConfigDisabled(t *testing.T) {
	got, fallback, err := tlsConfig(Config{SSLMode: "disabled"}, "db.example")
	if err != nil || got != nil || fallback {
		t.Errorf("tlsConfig(disabled) = %v, %v, %v; want nil, false, nil", got, fallback, err)
	}
}

func TestTLSConfigDefaultIsPreferred(t *testing.T) {
	got, fallback, err := tlsConfig(Config{}, "db.example")
	if err != nil || got == nil || !fallback || !got.InsecureSkipVerify {
		t.Errorf("tlsConfig(\"\") = %v, %v, %v; want TLS without verification and plaintext fallback", got, fallback, err)
	}
}

func TestTLSConfigPreferredOnSocketIsPlain(t *testing.T) {
	got, _, err := tlsConfig(Config{SSLMode: "preferred", Socket: "/tmp/s.sock"}, "")
	if err != nil || got != nil {
		t.Errorf("tlsConfig(preferred, socket) = %v, %v; want nil, nil", got, err)
	}
}

func TestTLSConfigRequiredHasNoFallback(t *testing.T) {
	got, fallback, err := tlsConfig(Config{SSLMode: "required"}, "db.example")
	if err != nil || got == nil || fallback || !got.InsecureSkipVerify || got.VerifyPeerCertificate != nil {
		t.Errorf("tlsConfig(required) = %+v, %v, %v; want unverified TLS without fallback", got, fallback, err)
	}
}

func TestTLSConfigVerifyIdentity(t *testing.T) {
	_, _, der := testCert(t, "ca", true, nil, nil)
	got, fallback, err := tlsConfig(Config{SSLMode: "verify-identity", SSLCA: writeCA(t, der)}, "db.example")
	if err != nil || got == nil || fallback {
		t.Fatalf("tlsConfig(verify-identity) = %v, %v, %v", got, fallback, err)
	}
	if got.InsecureSkipVerify || got.ServerName != "db.example" || got.RootCAs == nil {
		t.Errorf("verify-identity config = %+v, want verification on, ServerName db.example and RootCAs", got)
	}
}

func TestTLSConfigErrors(t *testing.T) {
	notPEM := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, der := testCert(t, "ca", true, nil, nil)
	ca := writeCA(t, der)
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"unknown mode", Config{SSLMode: "bogus"}, "ssl-mode"},
		{"missing CA file", Config{SSLMode: "verify-ca", SSLCA: filepath.Join(t.TempDir(), "none")}, "ssl-ca"},
		{"CA without certificates", Config{SSLMode: "verify-ca", SSLCA: notPEM}, "no certificates"},
		{"verify-identity on socket", Config{SSLMode: "verify-identity", Socket: "/tmp/s.sock"}, "socket"},
		{"verify-ca without CA", Config{SSLMode: "verify-ca"}, "ssl-ca"},
		{"CA with disabled", Config{SSLMode: "disabled", SSLCA: ca}, "ssl-ca"},
		{"CA with preferred", Config{SSLMode: "preferred", SSLCA: ca}, "ssl-ca"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := tlsConfig(tt.cfg, "db.example")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("tlsConfig error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestVerifyCAChecksChainButNotHostname exercises the verify-ca callback with
// a real chain: a certificate issued by the CA is accepted whatever its name,
// one issued by another CA is rejected.
func TestVerifyCAChecksChainButNotHostname(t *testing.T) {
	ca, caKey, caDER := testCert(t, "ca", true, nil, nil)
	_, _, leafDER := testCert(t, "other-host.example", false, ca, caKey)
	otherCA, otherKey, _ := testCert(t, "other-ca", true, nil, nil)
	_, _, strangerDER := testCert(t, "db.example", false, otherCA, otherKey)

	got, _, err := tlsConfig(Config{SSLMode: "verify-ca", SSLCA: writeCA(t, caDER)}, "db.example")
	if err != nil || got == nil || got.VerifyPeerCertificate == nil || !got.InsecureSkipVerify {
		t.Fatalf("tlsConfig(verify-ca) = %+v, %v", got, err)
	}
	if err := got.VerifyPeerCertificate([][]byte{leafDER}, nil); err != nil {
		t.Errorf("certificate from the trusted CA rejected: %v", err)
	}
	if err := got.VerifyPeerCertificate([][]byte{strangerDER}, nil); err == nil {
		t.Error("certificate from an untrusted CA accepted")
	}
	if err := got.VerifyPeerCertificate(nil, nil); err == nil {
		t.Error("empty certificate list accepted")
	}
}

// TestRequiredFailsWithoutServerTLS connects to a fake server whose handshake
// does not advertise CLIENT_SSL. With ssl-mode=required Open must fail rather
// than send credentials in clear text.
func TestRequiredFailsWithoutServerTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = c.Write(plainHandshake())
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1024)
		_, _ = c.Read(buf)
	}()

	addr := ln.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = Open(ctx, Config{Host: "127.0.0.1", Port: addr.Port, User: "u", SSLMode: "required"}, "app")
	if !errors.Is(err, mysql.ErrNoTLS) {
		t.Errorf("Open error = %v, want %v", err, mysql.ErrNoTLS)
	}
}

// plainHandshake builds a protocol-41 server handshake without CLIENT_SSL.
func plainHandshake() []byte {
	var p []byte
	p = append(p, 10)
	p = append(p, "8.0.0-fake\x00"...)
	p = append(p, 1, 0, 0, 0)                       // connection id
	p = append(p, "abcdefgh"...)                    // auth data, part 1
	p = append(p, 0)                                // filler
	p = binary.LittleEndian.AppendUint16(p, 0x0208) // caps low: PROTOCOL_41 | CONNECT_WITH_DB? no SSL (0x0800)
	p = append(p, 45)                               // charset
	p = binary.LittleEndian.AppendUint16(p, 2)      // status
	p = binary.LittleEndian.AppendUint16(p, 0x0008) // caps high: PLUGIN_AUTH
	p = append(p, 21)                               // auth data length
	p = append(p, make([]byte, 10)...)              // reserved
	p = append(p, "ijklmnopqrst\x00"...)            // auth data, part 2
	p = append(p, "mysql_native_password\x00"...)
	hdr := []byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), 0}
	return append(hdr, p...)
}

func queryCipher(t *testing.T, s *Snapshot) string {
	t.Helper()
	var name, value string
	if err := s.conn.QueryRowContext(context.Background(), "SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(&name, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSSLCipherByMode(t *testing.T) {
	cfg, db, _ := testDB(t)
	for _, tt := range []struct {
		mode    string
		wantTLS bool
	}{
		{"disabled", false},
		{"preferred", true},
		{"required", true},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			c := cfg
			c.SSLMode = tt.mode
			if c.Socket != "" && tt.mode != "required" {
				t.Skip("TLS is not used on a Unix socket unless required")
			}
			s := openSnapshot(t, c, db)
			if got := queryCipher(t, s); (got != "") != tt.wantTLS {
				t.Errorf("Ssl_cipher = %q with ssl-mode=%s, want non-empty=%v", got, tt.mode, tt.wantTLS)
			}
		})
	}
}
