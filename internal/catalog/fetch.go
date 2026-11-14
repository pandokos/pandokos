package catalog

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/pandokos/pandokos/internal/buildinfo"
)

const (
	maxDownload    = 16 << 20
	fetchTimeout   = 60 * time.Second
	dialTimeout    = 10 * time.Second
	fetchUserAgent = "pandokos/"
)

// FetchError is a failed download. Class is one of unreachable, timeout, status, redirect,
// too_large (and dataset_invalid when the caller adds it). It says nothing else: not the URL,
// not the error of the network, not a byte of the response.
type FetchError struct{ Class string }

// Error returns the class.
func (e *FetchError) Error() string { return "catalog: the price download failed: " + e.Class }

// FetchOptions configure Fetch.
type FetchOptions struct {
	Timeout time.Duration // the whole download; default 60 s
	roots   *x509.CertPool
}

var errFetchURL = errors.New("catalog: the price URL must be an https URL with a host and no user information")

// Fetch downloads the dataset at an https URL: a GET with the headers Accept and User-Agent and
// nothing else, through a transport of its own (no proxy, system roots, TLS 1.2 or later, no
// connection reuse: the transport lives for one download), no redirect followed, 60 s in all, status 200 only, at most 16 MiB.
func Fetch(ctx context.Context, rawURL string, o FetchOptions) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, errFetchURL
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = fetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.roots},
		TLSHandshakeTimeout: dialTimeout,
		DisableCompression:  true, // no Accept-Encoding: the request carries Accept and User-Agent only
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errFetchURL
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fetchUserAgent+buildinfo.Version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, &FetchError{Class: failureClass(ctx, err)}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, &FetchError{Class: "redirect"}
	case resp.StatusCode != http.StatusOK:
		return nil, &FetchError{Class: "status"}
	case resp.ContentLength > maxDownload:
		return nil, &FetchError{Class: "too_large"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, &FetchError{Class: failureClass(ctx, err)}
	}
	if len(body) > maxDownload {
		return nil, &FetchError{Class: "too_large"}
	}
	return body, nil
}

// failureClass tells a timeout from an unreachable host.
func failureClass(ctx context.Context, err error) string {
	var ne net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timeout"
	}
	return "unreachable"
}
