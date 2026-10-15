package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sync"
	"time"

	"github.com/justenwalker/kevin/internal/ca"
)

// certSigner mints a leaf certificate for a host on first use, signed by the
// project's intermediate authority, and caches it until half of its lifetime
// has passed.
type certSigner struct {
	authority *ca.CA
	now       func() time.Time

	mu    sync.Mutex
	cache map[string]cachedLeaf
}

type cachedLeaf struct {
	cert    *tls.Certificate
	renewAt time.Time
}

// newCertSigner builds a certSigner that mints a ServerAuth leaf per host,
// signed by authority.
func newCertSigner(authority *ca.CA) *certSigner {
	return &certSigner{authority: authority, now: time.Now, cache: make(map[string]cachedLeaf)}
}

// leafFor mints or returns a cached leaf for host. A leaf past half its
// lifetime is minted again, so a long run never serves an expired one.
func (s *certSigner) leafFor(host string) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if entry, ok := s.cache[host]; ok && now.Before(entry.renewAt) {
		return entry.cert, nil
	}
	cert, err := s.sign(host)
	if err != nil {
		return nil, err
	}
	s.cache[host] = cachedLeaf{cert: cert, renewAt: now.Add(ca.LeafLifetime / 2)}
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
