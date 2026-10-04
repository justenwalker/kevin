// Package netca builds the HTTP transport kevin uses to fetch plugin
// packages, trusting any extra roots the host names.
package netca

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

// EnvVar names a PEM file of extra root certificates for plugin package
// fetches.
const EnvVar = "KEVIN_PLUGIN_CA_FILE"

// Transport returns a clone of [http.DefaultTransport] whose root pool is the
// system pool plus the certificates in the file EnvVar names. With EnvVar
// unset it returns the clone unchanged.
func Transport() (*http.Transport, error) {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("netca: default transport is %T: %w", http.DefaultTransport, ErrCAFile)
	}
	t = t.Clone()
	path := os.Getenv(EnvVar)
	if path == "" {
		return t, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("netca: load system roots: %w: %w", ErrCAFile, err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own setting
	if err != nil {
		return nil, fmt.Errorf("netca: %s: %w: %w", EnvVar, ErrCAFile, err)
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("netca: %s: %q holds no PEM certificate: %w", EnvVar, path, ErrCAFile)
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = new(tls.Config)
	}
	t.TLSClientConfig.RootCAs = pool
	return t, nil
}
