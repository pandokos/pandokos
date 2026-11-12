package gateway

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// NewTransport returns the outbound transport of the gateway. The process environment does not
// redirect provider traffic (no proxy), certificates are always verified, and compression is
// not requested so that the usage reader sees plain bytes.
func NewTransport(connectTimeout time.Duration) *http.Transport {
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   connectTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
		ResponseHeaderTimeout: 0,
	}
}
