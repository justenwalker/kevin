// Package ca creates the certificate authorities of kevin.
//
//	m := ca.NewManager(cwd, "", "demo", ca.Options{})
//	root, err := m.LoadOrGenerateRoot()
//	project, err := m.LoadOrGenerateIntermediate()
//	signer, err := project.TLSCertificate()
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/justenwalker/kevin/internal/state"
)

// Manager loads or generates the root and per-project certificate
// authorities of kevin. The zero value is not usable. Call [NewManager].
type Manager struct {
	rootLifetime         time.Duration
	intermediateLifetime time.Duration
	rootDir              string
	projectName          string
	projectDir           string

	root *CA
}

// Options configures a Manager. A zero Options uses the default lifetimes.
type Options struct {
	RootLifetime         time.Duration
	IntermediateLifetime time.Duration
}

// NewManager creates a Manager for a project rooted at cwd. name isolates
// the manager's state directory for one named environment in cwd ("" for
// the unnamed environment, see [github.com/justenwalker/kevin/internal/config.Load]);
// project names the CA cert's CommonName.
func NewManager(cwd, name, project string, opt Options) *Manager {
	if opt.RootLifetime == 0 {
		opt.RootLifetime = rootLifetime
	}
	if opt.IntermediateLifetime == 0 {
		opt.IntermediateLifetime = intermediateLifetime
	}
	return &Manager{
		rootLifetime:         opt.RootLifetime,
		intermediateLifetime: opt.IntermediateLifetime,
		rootDir:              state.UserStateDir(),
		projectDir:           state.ProjectStateDir(cwd, name),
		projectName:          project,
	}
}

// File names inside an authority directory.
const (
	// CertFile holds the Certificate Authority Certificate of a project.
	CertFile = "ca.crt"

	// KeyFile holds the Certificate Authority private key of a project.
	KeyFile = "ca.key"

	// RootCertFile holds the Root Certificate.
	RootCertFile = "root.crt"

	// RootKeyFile holds the Root private key.
	RootKeyFile = "root.key"
)

// RootCertPath returns the host path of the Root Certificate file.
func RootCertPath() string {
	return filepath.Join(state.UserStateDir(), RootCertFile)
}

// RootCommonName is the common name of the Root Certificate Authority.
const RootCommonName = "Kevin Local Root CA"

// IntermediateCommonName is the common name of the Intermediate Certificate Authority.
const IntermediateCommonName = "Kevin Local Intermediate CA"

// Lifetimes of the two levels.
const (
	rootLifetime         = 10 * 365 * 24 * time.Hour
	intermediateLifetime = 2 * 365 * 24 * time.Hour

	// backdate covers a clock that runs a little behind on another machine or
	// inside a container.
	backdate = time.Hour
)

// CA is a certificate authority. A CA is safe for concurrent use.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte

	// rootPEM is the certificate that a client must trust. For a root it is
	// the same as certPEM.
	rootPEM []byte
}

// LoadOrGenerateRoot loads or generates the Root CA.
func (m *Manager) LoadOrGenerateRoot() (*CA, error) {
	ca, err := m.loadOrGenerateRoot(state.UserStateDir())
	if err != nil {
		return nil, err
	}
	m.root = ca
	return ca, nil
}

func (m *Manager) loadOrGenerateRoot(dir string) (*CA, error) {
	root, err := loadPair(dir, RootCertFile, RootKeyFile)
	switch {
	case err == nil:
		root.rootPEM = root.certPEM
		return root, nil
	case errors.Is(err, ErrNotFound):
		return m.generateRoot()
	default:
		return nil, err
	}
}

// LoadOrGenerateIntermediate loads or generates a project's Certificate Authority.
// If the project CA is found and is signed by the root CA, it is returned.
// Otherwise, a new CA is generated and signed by the root CA.
func (m *Manager) LoadOrGenerateIntermediate() (*CA, error) {
	if m.root == nil {
		_, err := m.LoadOrGenerateRoot()
		if err != nil {
			return nil, err
		}
	}
	return m.loadOrGenerateIntermediate()
}

func (m *Manager) loadOrGenerateIntermediate() (*CA, error) {
	authority, err := loadPair(m.projectDir, CertFile, KeyFile)
	switch {
	case err == nil:
		authority.rootPEM = m.root.certPEM
		// A root that changed leaves an intermediate that no client can
		// verify. Sign a new one.
		if authority.cert.CheckSignatureFrom(m.root.cert) == nil {
			return authority, nil
		}
	case errors.Is(err, ErrNotFound):
	default:
		return nil, err
	}
	return m.generateIntermediate()
}

func (m *Manager) generateRoot() (*CA, error) {
	template := baseTemplate(RootCommonName, m.rootLifetime)
	// One level below the root, which is the authority of a project.
	template.MaxPathLen = 1

	authority, err := create(template, nil)
	if err != nil {
		return nil, err
	}
	authority.rootPEM = authority.certPEM

	if err = write(m.rootDir, map[string][]byte{
		RootCertFile: authority.certPEM,
		RootKeyFile:  authority.keyPEM,
	}); err != nil {
		return nil, err
	}
	return authority, nil
}

func (m *Manager) generateIntermediate() (*CA, error) {
	template := baseTemplate(fmt.Sprintf("%s - Project %s", IntermediateCommonName, m.projectName), m.intermediateLifetime)
	// This authority signs leaves only.
	template.MaxPathLen = 0
	template.MaxPathLenZero = true

	authority, err := create(template, m.root)
	if err != nil {
		return nil, err
	}
	authority.rootPEM = m.root.certPEM

	// The certificate file carries the chain so that the proxy serves the
	// intermediate along with each leaf.
	chain := append(append([]byte{}, authority.certPEM...), m.root.certPEM...)
	authority.certPEM = chain

	if err = write(m.projectDir, map[string][]byte{
		CertFile:     chain,
		KeyFile:      authority.keyPEM,
		RootCertFile: m.root.certPEM,
	}); err != nil {
		return nil, err
	}
	return authority, nil
}

// baseTemplate builds the common parts of an authority certificate.
func baseTemplate(commonName string, lifetime time.Duration) *x509.Certificate {
	now := time.Now()
	return &x509.Certificate{
		// SerialNumber: nil, // This will be automatically set by x509.CreateCertificate
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"Kevin"},
		},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
}

// create signs a template. A nil parent produces a self-signed certificate.
func create(template *x509.Certificate, parent *CA) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate key: %w", err)
	}

	issuer, signer := template, any(key)
	if parent != nil {
		issuer, signer = parent.cert, parent.key
	}

	der, err := x509.CreateCertificate(rand.Reader, template, issuer, &key.PublicKey, signer)
	if err != nil {
		return nil, fmt.Errorf("ca: create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ca: marshal key: %w", err)
	}

	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// write puts the files of an authority into dir.
func write(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ca: create %s: %w", dir, err)
	}
	for name, data := range files {
		mode := os.FileMode(0o644)
		if filepath.Ext(name) == ".key" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return fmt.Errorf("ca: write %s: %w", name, err)
		}
	}
	return nil
}

// loadPair reads a certificate and a key from dir.
func loadPair(dir, certFile, keyFile string) (*CA, error) {
	certPEM, err := readFile(dir, certFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := readFile(dir, keyFile)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("ca: %s holds no PEM block: %w", certFile, ErrInvalid)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse %s: %w: %w", certFile, ErrInvalid, err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("ca: %s is not an authority: %w", certFile, ErrInvalid)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("ca: %s holds no PEM block: %w", keyFile, ErrInvalid)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse %s: %w: %w", keyFile, ErrInvalid, err)
	}

	return &CA{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

func readFile(dir, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // an authority of this tool
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ca: read %s: %w", name, err)
	}
	return data, nil
}

// PEM returns the certificate of this authority. For a project it is the
// chain: the authority of the project, then the root.
func (c *CA) PEM() string { return string(c.certPEM) }

// RootPEM returns the certificate that a client must trust.
func (c *CA) RootPEM() string { return string(c.rootPEM) }

// Certificate returns the parsed certificate of this authority.
func (c *CA) Certificate() *x509.Certificate { return c.cert }

// Pool returns a certificate pool that trusts the root alone.
func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(c.rootPEM)
	return pool
}

// TLSCertificate returns this authority as a signing certificate.
func (c *CA) TLSCertificate() (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(c.certPEM, c.keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("ca: build the signing certificate: %w", err)
	}
	return cert, nil
}
