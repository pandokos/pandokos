// Command testcert writes a throw-away certificate authority and a server certificate into a
// directory, for test/db/up.sh. It prints no key material.
package main

import (
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pandokos/pandokos/test/testcert"
)

func main() {
	dir := flag.String("dir", "", "directory for ca.pem, server.pem and server.key (required)")
	dns := flag.String("dns", "", "comma-separated DNS names")
	ip := flag.String("ip", "", "comma-separated IP addresses")
	days := flag.Int("days", 30, "validity in days")
	flag.Parse()
	if *dir == "" || *days < 1 {
		fmt.Fprintln(os.Stderr, "usage: testcert -dir DIR [-dns NAME,...] [-ip ADDR,...] [-days N]")
		os.Exit(2)
	}
	if current(filepath.Join(*dir, "server.pem")) {
		fmt.Fprintln(os.Stdout, "certificate is current")
		return
	}
	var ips []net.IP
	for _, s := range split(*ip) {
		a := net.ParseIP(s)
		if a == nil {
			fmt.Fprintln(os.Stderr, "testcert: not an IP address:", s)
			os.Exit(2)
		}
		ips = append(ips, a)
	}
	validFor := time.Duration(*days) * 24 * time.Hour
	ca, err := testcert.NewCA(validFor)
	if err == nil {
		now := time.Now()
		var certPEM, keyPEM []byte
		certPEM, keyPEM, err = ca.Issue(split(*dns), ips, now.Add(-time.Minute), now.Add(validFor))
		if err == nil {
			_, _, _, err = testcert.WriteFiles(*dir, ca, certPEM, keyPEM)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "testcert:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "wrote %s/ca.pem, server.pem, server.key\n", *dir)
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// current reports whether file holds a certificate that is valid for more than 24 hours.
func current(file string) bool {
	b, err := os.ReadFile(file) //nolint:gosec // the path is a flag of a test tool
	if err != nil {
		return false
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return false
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	now := time.Now()
	return !now.Before(c.NotBefore) && c.NotAfter.After(now.Add(24*time.Hour))
}
