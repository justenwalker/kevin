package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Environment variables the engine sets on the relay container to carry its
// mutual-TLS control channel material. Mirrors internal/relay's identical
// constants - not importable here, the relay binary sits above internal/relay
// in the dependency graph.
const (
	tlsCertEnv     = "KEVIN_RELAY_TLS_CERT"
	tlsKeyEnv      = "KEVIN_RELAY_TLS_KEY"
	tlsClientCAEnv = "KEVIN_RELAY_TLS_CLIENT_CA"
)

// controlServerCN is the common name the engine mints the relay's control
// server certificate for. A client dialing the control endpoint sets this
// as its TLS ServerName, since the dialed address is always a loopback
// port, never a name the certificate itself could carry. Mirrors
// internal/relay's identical constant - not importable here, see the env
// var comment above.
const controlServerCN = "kevin-relay-control"

// controlTLSConfig builds the mutual-TLS server config for the control gRPC
// server from the certificate material the engine embeds in the container's
// environment: a server certificate chained to the project's authority, and
// the project's root as the pool a caller's own client certificate must
// chain to.
func controlTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(os.Getenv(tlsCertEnv)), []byte(os.Getenv(tlsKeyEnv)))
	if err != nil {
		return nil, fmt.Errorf("relay: parse control tls certificate: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(os.Getenv(tlsClientCAEnv))) {
		return nil, fmt.Errorf("relay: %w", ErrInvalidControlClientCA)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}
