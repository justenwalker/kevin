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
//	})
//	defer r.Close()
package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/version"
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
}

// Relay is a running relay container.
type Relay struct {
	name string
	addr string
}

// Start creates the relay container, or reuses one already running for
// opts.Project whose recorded Domain/ProxyAddr still match (see reusable).
func Start(ctx context.Context, opts Options) (*Relay, error) {
	name := containerName(opts.Project)
	client := docker.Client{}

	if r, err := reusable(ctx, client, name, opts); err != nil {
		return nil, err
	} else if r != nil {
		return r, nil
	}

	// Absent, stopped, or drifted - remove it first, so a second Start does
	// not fail on the name.
	if err := client.Remove(ctx, name); err != nil {
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
		Cmd:      []string{"forward", "--domain", opts.Domain, "--proxy", opts.ProxyAddr},
	}
	if _, err := client.Run(ctx, spec); err != nil {
		return nil, err
	}

	info, err := client.Inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	addr, ok := info.IPs[opts.Network]
	if !ok {
		return nil, fmt.Errorf("relay: %w", ErrNoAddress)
	}

	return &Relay{name: name, addr: addr}, nil
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
	addr, ok := info.IPs[network]
	if !ok {
		return nil, fmt.Errorf("relay: %w", ErrNoAddress)
	}
	return &Relay{name: name, addr: addr}, nil
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
	addr, ok := info.IPs[opts.Network]
	if !ok {
		return nil, fmt.Errorf("relay: %w", ErrNoAddress)
	}
	return &Relay{name: name, addr: addr}, nil
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

// Addr is the address of the relay container on the shared network. A
// workload uses it for DNS.
func (r *Relay) Addr() string { return r.addr }

// Close removes the relay container. Close is idempotent.
func (r *Relay) Close() error { return (docker.Client{}).Remove(context.Background(), r.name) }

// containerName builds the container name for the relay of one project.
func containerName(project string) string { return "kevin-" + project + "-relay" }
