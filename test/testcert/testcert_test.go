package testcert_test

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testcert"
)

func TestIssueVerifiesAgainstTheCA(t *testing.T) {
	ca, err := testcert.NewCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	certPEM, keyPEM, err := ca.Issue([]string{"pandokos-test-db"}, []net.IP{net.ParseIP("127.0.0.1")}, now.Add(-time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("no certificate block")
	}
	if kb, _ := pem.Decode(keyPEM); kb == nil || kb.Type != "PRIVATE KEY" {
		t.Fatal("the key is not a PKCS #8 block")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("CA PEM not accepted")
	}
	for name, opts := range map[string]x509.VerifyOptions{
		"dns name": {Roots: pool, DNSName: "pandokos-test-db"},
		"ip":       {Roots: pool, DNSName: "127.0.0.1"},
	} {
		if _, err := leaf.Verify(opts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err == nil {
		t.Error("a name that is not listed must not verify")
	}
}

func TestFilesWritesTheKeyPrivately(t *testing.T) {
	caFile, certFile, keyFile := testcert.Files(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	for _, f := range []string{caFile, certFile, keyFile} {
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(keyFile)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("key mode = %v, want 0600", st.Mode().Perm())
		}
	}
}
