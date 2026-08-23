package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

// leafLifetime is how long a minted leaf is valid.
const leafLifetime = 7 * 24 * time.Hour

// certSigner mints a leaf certificate for a host on first use, signed by the
// project's intermediate authority, and caches it for the life of the
// process.
type certSigner struct {
	issuer *x509.Certificate
	key    *ecdsa.PrivateKey
	chain  [][]byte // the issuer's own chain, appended after every leaf

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// newCertSigner builds a certSigner from the signing certificate
// [ca.CA.TLSCertificate] returns.
func newCertSigner(signer tls.Certificate) (*certSigner, error) {
	if len(signer.Certificate) == 0 {
		return nil, ErrNoSigningCertificate
	}
	issuer, err := x509.ParseCertificate(signer.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("proxy: parse signing certificate: %w", err)
	}
	key, ok := signer.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, ErrUnsupportedSigningKey
	}
	return &certSigner{
		issuer: issuer,
		key:    key,
		chain:  signer.Certificate,
		cache:  make(map[string]*tls.Certificate),
	}, nil
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
// turn, the root - the same chain [ca.CA.TLSCertificate] carries).
func (s *certSigner) sign(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("proxy: generate leaf key for %q: %w", host, err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("proxy: leaf serial for %q: %w", host, err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, s.issuer, &key.PublicKey, s.key)
	if err != nil {
		return nil, fmt.Errorf("proxy: sign leaf for %q: %w", host, err)
	}

	chain := make([][]byte, 0, 1+len(s.chain))
	chain = append(chain, der)
	chain = append(chain, s.chain...)
	return &tls.Certificate{Certificate: chain, PrivateKey: key}, nil
}
