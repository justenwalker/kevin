package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sync"

	"github.com/justenwalker/kevin/internal/ca"
)

// certSigner mints a leaf certificate for a host on first use, signed by the
// project's intermediate authority, and caches it for the life of the
// process.
type certSigner struct {
	authority *ca.CA

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// newCertSigner builds a certSigner that mints a ServerAuth leaf per host,
// signed by authority.
func newCertSigner(authority *ca.CA) *certSigner {
	return &certSigner{authority: authority, cache: make(map[string]*tls.Certificate)}
}

// leafFor mints or returns a cached leaf for host.
func (s *certSigner) leafFor(host string) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cert, ok := s.cache[host]; ok {
		return cert, nil
	}
	cert, err := s.sign(host)
	if err != nil {
		return nil, err
	}
	s.cache[host] = cert
	return cert, nil
}

// sign issues a new leaf for host, chained after the intermediate (and, in
// turn, the root).
func (s *certSigner) sign(host string) (*tls.Certificate, error) {
	cert, err := s.authority.NewLeaf(host, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, ca.LeafLifetime)
	if err != nil {
		return nil, fmt.Errorf("proxy: sign leaf for %q: %w", host, err)
	}
	return &cert, nil
}
