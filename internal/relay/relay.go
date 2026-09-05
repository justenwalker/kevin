// Package relay creates and removes the relay container of one project.
//
// The relay runs cmd/kevin-relay inside the shared docker network. A
// workload reaches a step under the environment domain through the relay,
// with no proxy environment variables of its own.
//
//	r, err := relay.Start(ctx, relay.Options{
//		Project:   "demo",
//		Network:   "kevin-demo",
//		Domain:    "kevin.home",
//		ProxyAddr: "host.docker.internal:18080",
//		Image:     relay.Ref(""),
//		Authority: authority,
//	})
//	defer r.Close()
package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/version"
	"github.com/justenwalker/kevin/protos/pb"
)

// imageRepo is the registry path .goreleaser.yaml publishes kevin-relay to.
const imageRepo = "ghcr.io/justenwalker/kevin/relay"

// Image is the relay image kevin runs by default: imageRepo tagged with
// kevin's own version, or the image relay-image builds locally when
// version.String is still "dev".
var Image = defaultImage()

func defaultImage() string {
	if version.String == "dev" {
		return "kevin-relay:dev"
	}
	// .goreleaser.yaml tags the image with GoReleaser's {{ .Version }},
	// which strips the leading "v" that version.String keeps (Go modules
	// require it on the tag, e.g. `go install ...@v0.0.1`).
	return imageRepo + ":" + strings.TrimPrefix(version.String, "v")
}

// ImageEnvVar overrides the relay image outright. Set it to run a relay
// image that a shared kevin.cue does not name.
const ImageEnvVar = "KEVIN_RELAY_IMAGE"

// RepoEnvVar overrides just the repository of whichever image would
// otherwise apply, keeping its tag - for mirroring imageRepo to a private
// registry without also having to track and override the version tag.
const RepoEnvVar = "KEVIN_RELAY_REPO"

// TagEnvVar overrides just the tag of whichever image would otherwise
// apply, keeping its repository.
const TagEnvVar = "KEVIN_RELAY_TAG"

// Role marks a container as the relay. Pass it as the value of
// [cri.LabelRole].
const Role = "relay"

// hostGateway is the name that Docker resolves to the host from inside a
// container.
const hostGateway = "host.docker.internal"

// domainLabel and proxyAddrLabel record the Domain/ProxyAddr a relay
// container was started with, so a later Start can tell whether they
// still match its own opts before reusing the container.
const (
	domainLabel    = "kevin.relay.domain"
	proxyAddrLabel = "kevin.relay.proxy"
)

// Ref resolves the relay image to run. [ImageEnvVar] in the process
// environment wins outright over everything else. Otherwise, configured
// wins over [Image], and then [RepoEnvVar] and [TagEnvVar] each
// independently override their piece of the result.
func Ref(configured string) string {
	if v := os.Getenv(ImageEnvVar); v != "" {
		return v
	}

	image := Image
	if configured != "" {
		image = configured
	}

	repo, tag := splitImage(image)
	if v := os.Getenv(RepoEnvVar); v != "" {
		repo = v
	}
	if v := os.Getenv(TagEnvVar); v != "" {
		tag = v
	}
	if tag == "" {
		return repo
	}
	return repo + ":" + tag
}

// splitImage splits a "repo:tag" image reference into its two parts. A
// colon before the last "/" is part of a registry:port, not a tag
// separator (e.g. "registry.local:5000/kevin-relay" has no tag).
func splitImage(image string) (string, string) {
	i := strings.LastIndex(image, ":")
	if i < 0 || strings.Contains(image[i:], "/") {
		return image, ""
	}
	return image[:i], image[i+1:]
}

// Options configure [Start].
type Options struct {
	// Project names the environment. Project prefixes the container name.
	Project string

	// Network is the shared docker network. The relay joins this network and
	// resolves its own address on it.
	Network string

	// Domain is the base name of the environment. The relay answers an A
	// query for a name under this domain with its own address.
	Domain string

	// ProxyAddr is the address of the host proxy that the relay forwards
	// traffic to.
	ProxyAddr string

	// Image is the relay image to run. Call [Ref] to resolve it.
	Image string

	// Scope is which DAG started this relay ("setup" or "env"), recorded
	// as the "kevin.scope" label. A reused container keeps its original.
	Scope string

	// Authority signs the relay's control-channel server certificate, and
	// the client certificate Start uses to call it - both leaves off the
	// project's own intermediate authority, the same one that signs MITM
	// leaves.
	Authority *ca.CA
}

// controlServerCN and controlClientCN name the leaves Start mints for the
// control channel's mutual TLS. controlServerCN also doubles as the TLS
// ServerName a caller dials with, since the dialed address is always a
// loopback port, never a name the certificate itself could carry. Mirrors
// cmd/kevin-relay's identical constants - not importable there, the relay
// binary sits below this package in the dependency graph.
const (
	controlServerCN = "kevin-relay-control"
	controlClientCN = "kevin-engine-control"
)

// Environment variables Start embeds in the relay container's environment
// to carry its control-channel TLS material. Mirrors cmd/kevin-relay's
// identical constants.
const (
	tlsCertEnv     = "KEVIN_RELAY_TLS_CERT"
	tlsKeyEnv      = "KEVIN_RELAY_TLS_KEY"
	tlsClientCAEnv = "KEVIN_RELAY_TLS_CLIENT_CA"
)

// socks5Port is the fixed container port the relay's SOCKS5 gateway
// listens on, published to the host loopback on an OS-assigned port.
const socks5Port = "1080/tcp"

// controlPort is the fixed container port the relay's control gRPC server
// listens on, published to the host loopback on an OS-assigned port - the
// same reason socks5Port is published rather than reached over the docker
// network: the engine calling EnsureListener or RegisterCapture is a native
// host process, with the same VM-boundary limits that keep it from dialing
// a container's docker-network address directly.
const controlPort = "8053/tcp"

// Relay is a running relay container.
type Relay struct {
	name        string
	addr        string
	socks5Addr  string
	controlAddr string

	conn   *grpc.ClientConn
	client pb.RelayControlClient
}

// Start creates the relay container, or reuses one already running for
// opts.Project whose recorded Domain/ProxyAddr still match (see reusable),
// then dials its control channel.
func Start(ctx context.Context, opts Options) (*Relay, error) {
	name := containerName(opts.Project)
	client := docker.Client{}

	r, err := reusable(ctx, client, name, opts)
	if err != nil {
		return nil, err
	}
	if r == nil {
		r, err = create(ctx, client, name, opts)
		if err != nil {
			return nil, err
		}
	}

	if err := r.dialControl(opts.Authority); err != nil {
		return nil, err
	}
	return r, nil
}

// create removes any absent, stopped, or drifted container behind name,
// then starts a fresh relay container for opts.
func create(ctx context.Context, client docker.Client, name string, opts Options) (*Relay, error) {
	// Absent, stopped, or drifted - remove it first, so Run does not fail on
	// the name.
	if err := client.Remove(ctx, name); err != nil {
		return nil, err
	}

	serverCert, serverKey, err := controlServerPEM(opts.Authority)
	if err != nil {
		return nil, err
	}

	spec := cri.RunSpec{
		Image:   opts.Image,
		Name:    name,
		Network: opts.Network,
		Labels: map[string]string{
			cri.LabelProject: opts.Project,
			cri.LabelRole:    Role,
			cri.LabelScope:   cri.ScopeLabel(opts.Project, opts.Scope),
			domainLabel:      opts.Domain,
			proxyAddrLabel:   opts.ProxyAddr,
		},
		// Docker Desktop resolves host.docker.internal on its own. Plain Linux
		// Docker does not, so the relay needs the entry to reach the host proxy.
		AddHosts: []string{hostGateway + ":host-gateway"},
		// CAP_SYS_ADMIN lets the relay setns() into a target container's own
		// network namespace; CAP_NET_ADMIN lets it install nftables rules
		// once there. PidHost is what makes a namespace reachable by path at
		// all: a container's own /proc/<pid>/ns/net, opened by a PID the
		// relay's own host-shared PID namespace can see - not a bind mount
		// of /var/run/docker/netns, which on at least one Docker runtime
		// (OrbStack) can't setns() into a path that appeared in that
		// directory after the mount was already attached, only read it.
		// CAP_SYS_PTRACE is what actually lets that open succeed against a
		// --privileged target (a builtin:kind node, say): the kernel's
		// cross-process /proc/<pid>/ns/* access check treats a privileged
		// container's process as non-dumpable, which blocks it without that
		// capability even though CAP_SYS_ADMIN plus PidHost alone are enough
		// for an ordinary container. Accepted, not solved here: a
		// compromised relay could in principle setns() into an unrelated,
		// non-kevin process's namespace on the same host, since none of
		// these capabilities carry a finer-grained ACL. Relay only ever
		// acts on a path the engine itself handed it over the control
		// channel.
		CapAdd:  []string{"NET_ADMIN", "SYS_ADMIN", "SYS_PTRACE"},
		PidHost: true,
		Env: map[string]string{
			tlsCertEnv:     serverCert,
			tlsKeyEnv:      serverKey,
			tlsClientCAEnv: opts.Authority.RootPEM(),
		},
		Cmd: []string{"forward", "--domain", opts.Domain, "--proxy", opts.ProxyAddr},
		// The SOCKS5 gateway and the control endpoint are the two things on
		// the relay a host process needs to dial directly - everything else
		// (DNS, HTTP/HTTPS forwarding) is reached only from inside the
		// docker network.
		Ports: []string{"127.0.0.1::1080", "127.0.0.1::8053"},
	}
	if _, runErr := client.Run(ctx, spec); runErr != nil {
		return nil, runErr
	}

	info, err := client.Inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	return relayFromInfo(name, opts.Network, info)
}

// Lookup reports the relay container already running for project, without
// creating one. It returns (nil, nil) when no such container is running -
// the read-only counterpart to Start, for a caller (kevin teardown) that
// wants to close an existing relay but must never bring one up itself.
func Lookup(ctx context.Context, project, network string) (*Relay, error) {
	return lookup(ctx, docker.Client{}, containerName(project), network)
}

// lookup reports the running relay container named name on network, or
// (nil, nil) when it is absent or not running - a crash can leave a
// stopped container behind, which Start's caller must still replace.
func lookup(ctx context.Context, client docker.Client, name, network string) (*Relay, error) {
	info, err := inspectRunning(ctx, client, name)
	if err != nil || info == nil {
		return nil, err
	}
	return relayFromInfo(name, network, *info)
}

// reusable reports the relay container already running for name when its
// recorded Domain/ProxyAddr match opts, or (nil, nil) otherwise.
func reusable(ctx context.Context, client docker.Client, name string, opts Options) (*Relay, error) {
	info, err := inspectRunning(ctx, client, name)
	if err != nil || info == nil {
		return nil, err
	}
	if info.Labels[domainLabel] != opts.Domain || info.Labels[proxyAddrLabel] != opts.ProxyAddr {
		return nil, nil //nolint:nilnil // a drifted container is not reusable, same as an absent one
	}
	return relayFromInfo(name, opts.Network, *info)
}

// inspectRunning reports name's container info, or (nil, nil) when it is
// absent or not running - a crash can leave a stopped container behind.
func inspectRunning(ctx context.Context, client docker.Client, name string) (*cri.Container, error) {
	info, err := client.Inspect(ctx, name)
	if err != nil {
		if errors.Is(err, cri.ErrNotFound) {
			return nil, nil //nolint:nilnil // "no such container" is a documented, valid result
		}
		return nil, err
	}
	if !info.Running {
		return nil, nil //nolint:nilnil // a stopped leftover is treated the same as absent
	}
	return &info, nil
}

// relayFromInfo builds a Relay from name's inspected container info,
// reporting the address and published ports every caller (Start, lookup,
// reusable) needs.
func relayFromInfo(name, network string, info cri.Container) (*Relay, error) {
	addr, ok := info.IPs[network]
	if !ok {
		return nil, fmt.Errorf("relay: %w", ErrNoAddress)
	}
	socks5Addr, ok := info.Ports[socks5Port]
	if !ok {
		return nil, fmt.Errorf("relay: %w", ErrNoSOCKS5Addr)
	}
	controlAddr, ok := info.Ports[controlPort]
	if !ok {
		return nil, fmt.Errorf("relay: %w", ErrNoControlAddr)
	}
	return &Relay{name: name, addr: addr, socks5Addr: socks5Addr, controlAddr: controlAddr}, nil
}

// Addr is the address of the relay container on the shared network. A
// workload uses it for DNS.
func (r *Relay) Addr() string { return r.addr }

// SOCKS5Addr is the host-reachable address of the relay's SOCKS5 gateway. A
// step's Up builds a "socks5://<addr>/<target>" upstream against it to
// reach a docker-network address through a single host port instead of a
// dedicated published port.
func (r *Relay) SOCKS5Addr() string { return r.socks5Addr }

// Close removes the relay container, and the control channel's client
// connection, if one was dialed. Close is idempotent.
func (r *Relay) Close() error {
	if r.conn != nil {
		_ = r.conn.Close()
	}
	return (docker.Client{}).Remove(context.Background(), r.name)
}

// EnsureListener registers a route's External entry with the relay: host
// resolves to the relay's own address - the only way a workload with no
// network namespace the relay can capture (a kind pod) reaches the
// interception - and the relay opens a listener for each of ports beyond
// its always-on 80 and 443.
func (r *Relay) EnsureListener(ctx context.Context, host string, ports []int) error {
	ports32 := make([]int32, len(ports))
	for i, p := range ports {
		ports32[i] = int32(p) //nolint:gosec // a port number always fits in int32
	}
	req := &pb.EnsureListenerRequest{Host: host, Ports: ports32}
	if _, err := r.client.EnsureListener(ctx, req); err != nil {
		return fmt.Errorf("relay: ensure listener: %w", err)
	}
	return nil
}

// RegisterCapture tells the relay to redirect the outbound traffic of the
// container at netnsPath to itself, so id's egress is captured
// transparently.
func (r *Relay) RegisterCapture(ctx context.Context, id, netnsPath string) error {
	req := &pb.RegisterCaptureRequest{Id: id, NetnsPath: netnsPath}
	if _, err := r.client.RegisterCapture(ctx, req); err != nil {
		return fmt.Errorf("relay: register capture for %q: %w", id, err)
	}
	return nil
}

// dialControl mints a client leaf off authority and dials the relay's
// control endpoint with it, verifying the relay's own leaf against
// authority's root. Lazy: grpc.NewClient does not block on a connection, so
// this succeeds even against a relay whose control server isn't actually
// serving - such as the fixture image relay_test.go runs against - and any
// real failure only surfaces on the first RPC.
func (r *Relay) dialControl(authority *ca.CA) error {
	clientCert, err := authority.NewLeaf(controlClientCN, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, ca.LeafLifetime)
	if err != nil {
		return fmt.Errorf("relay: mint control client certificate: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      authority.Pool(),
		ServerName:   controlServerCN,
	}
	conn, err := grpc.NewClient(r.controlAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return fmt.Errorf("relay: dial control endpoint: %w", err)
	}
	r.conn = conn
	r.client = pb.NewRelayControlClient(conn)
	return nil
}

// controlServerPEM mints the relay's control-channel server leaf off
// authority, and PEM-encodes its certificate chain and key for embedding in
// the container's environment.
func controlServerPEM(authority *ca.CA) (string, string, error) {
	cert, err := authority.NewLeaf(controlServerCN, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, ca.LeafLifetime)
	if err != nil {
		return "", "", fmt.Errorf("relay: mint control server certificate: %w", err)
	}

	var certBuf strings.Builder
	for _, der := range cert.Certificate {
		if encErr := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); encErr != nil {
			return "", "", fmt.Errorf("relay: encode control server certificate: %w", encErr)
		}
	}

	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return "", "", fmt.Errorf("relay: %w", ErrUnsupportedControlKey)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("relay: marshal control server key: %w", err)
	}
	keyPEMBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certBuf.String(), string(keyPEMBytes), nil
}

// containerName builds the container name for the relay of one project.
func containerName(project string) string { return "kevin-" + project + "-relay" }
