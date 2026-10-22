// Package httpclient builds the outbound HTTP clients used for checks,
// scans and icon downloads.
package httpclient

import (
	"crypto/tls"
	"errors"
	"net/http"
	"time"
)

// LimitRedirects follows at most 5 redirects, to http(s) only.
func LimitRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return errors.New("redirect to unsupported scheme")
	}
	return nil
}

// NoRedirects returns the first response without following redirects.
func NoRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// New returns a client with its own transport. verifyTLS=false is for
// self-signed internal hosts and for probes that never trust content.
func New(timeout time.Duration, verifyTLS bool, redirect func(*http.Request, []*http.Request) error) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: !verifyTLS} //nolint:gosec // configurable, see CHECK_VERIFY_TLS
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: redirect}
}
