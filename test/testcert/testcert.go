// Package testcert is the one certificate generator of the test suite: a throw-away certificate
// authority and the server certificates it signs. It uses only the standard library. Nothing it
// makes is ever committed: test/db/up.sh writes into the git-ignored local/ directory and tests
// write into their temporary directory.
package testcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CA is a throw-away certificate authority (ECDSA P-256).
type CA struct {
	cert *x509.Certificate
	der  []byte
	key  *ecdsa.PrivateKey
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// NewCA returns a new authority that is valid from one minute ago for validFor.
func NewCA(validFor time.Duration) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "pandokos test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(validFor),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, der: der, key: key}, nil
}

// CertPEM returns the certificate of the authority as PEM.
func (c *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})
}

// Issue returns a server certificate and its key, as PEM, for the given DNS names and IP
// addresses, valid from notBefore to notAfter.
func (c *CA) Issue(dns []string, ips []net.IP, notBefore, notAfter time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "pandokos test server"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// WriteFiles writes ca.pem and server.pem (mode 0644) and server.key (mode 0600) into dir,
// which it creates (mode 0755) when it does not exist, and returns the three paths.
func WriteFiles(dir string, ca *CA, certPEM, keyPEM []byte) (caFile, certFile, keyFile string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // a certificate directory is read by the database container
		return "", "", "", err
	}
	caFile = filepath.Join(dir, "ca.pem")
	certFile = filepath.Join(dir, "server.pem")
	keyFile = filepath.Join(dir, "server.key")
	files := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{caFile, ca.CertPEM(), 0o644},
		{certFile, certPEM, 0o644},
		{keyFile, keyPEM, 0o600},
	}
	for _, f := range files {
		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			return "", "", "", err
		}
		if err := os.Chmod(f.path, f.mode); err != nil {
			return "", "", "", err
		}
	}
	return caFile, certFile, keyFile, nil
}

// Files writes ca.pem, server.pem and server.key (mode 0600) into tb.TempDir() for the given
// DNS names and IP addresses and returns the paths. The certificate is valid from one minute
// ago for one hour.
func Files(tb testing.TB, dns []string, ips []net.IP) (caFile, certFile, keyFile string) {
	tb.Helper()
	ca, err := NewCA(time.Hour)
	if err != nil {
		tb.Fatal(err)
	}
	now := time.Now()
	certPEM, keyPEM, err := ca.Issue(dns, ips, now.Add(-time.Minute), now.Add(time.Hour))
	if err != nil {
		tb.Fatal(err)
	}
	caFile, certFile, keyFile, err = WriteFiles(tb.TempDir(), ca, certPEM, keyPEM)
	if err != nil {
		tb.Fatal(err)
	}
	return caFile, certFile, keyFile
}
