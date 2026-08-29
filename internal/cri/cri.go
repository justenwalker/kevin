// Package cri is the contract that every container engine implements. It
// carries no engine-specific code: internal/docker is one implementation of
// [Runtime].
package cri

import (
	"context"
	"io"
)

// LabelPrefix starts every label that kevin puts on a resource.
const LabelPrefix = "kevin."

// LabelProject, LabelScope, and LabelURN name a resource at increasing
// granularity: "<project>", "<project>:<scope>", "<project>:<scope>:<step>".
// [ScopeLabel] and [URNLabel] build the latter two; [Runtime.ListByLabel]
// matches on exact value, so each tier is its own targeted query.
//
// LabelRole marks the purpose of a resource that no step owns, such as the
// relay container.
const (
	LabelProject = LabelPrefix + "project"
	LabelScope   = LabelPrefix + "scope"
	LabelURN     = LabelPrefix + "urn"
	LabelRole    = LabelPrefix + "role"
)

// ScopeLabel builds the value of [LabelScope]: project qualified by scope.
func ScopeLabel(project, scope string) string { return project + ":" + scope }

// URNLabel builds the value of [LabelURN]: project qualified by scope and
// step name - the full identity of one step's resource.
func URNLabel(project, scope, step string) string { return project + ":" + scope + ":" + step }

// RunSpec describes one container to create.
type RunSpec struct {
	// Image is the image reference. The field is required.
	Image string

	// Name is the container name. The field is required, because removal and
	// inspection use the name.
	Name string

	// Network is the network that the container joins.
	Network string

	// Alias adds a name for the container on the network, on top of Name.
	Alias string

	// Labels mark the container as owned by kevin.
	Labels map[string]string

	// Env holds the environment of the container.
	Env map[string]string

	// Ports publish a container port on the host, such as "8080:80".
	Ports []string

	// Volumes mount a host path, such as "/src:/dst:ro".
	Volumes []string

	// DNS names a DNS server for the container, such as "127.0.0.11".
	DNS []string

	// AddHosts adds an entry to /etc/hosts inside the container, such as
	// "web.kevin.home:172.20.0.5".
	AddHosts []string

	// Cmd replaces the command of the image.
	Cmd []string

	// Entrypoint replaces the entrypoint of the image, such as ["sh", "-c"].
	// Empty keeps the image's own entrypoint. Only the first element becomes
	// the process docker execs; the rest are prepended to Cmd as its first
	// arguments, since docker run's --entrypoint flag itself takes a single
	// executable.
	Entrypoint []string

	// Pull fetches the image before the container starts.
	Pull bool
}

// Container holds the parts of an inspect result that kevin uses.
type Container struct {
	ID      string
	Name    string
	Running bool

	// ExitCode is meaningful only when Running is false.
	ExitCode int

	// IPs maps a network name to the address of the container on it.
	IPs map[string]string

	// Ports maps a container port such as "80/tcp" to the host address.
	Ports map[string]string

	// Labels holds every label that the container carries.
	Labels map[string]string
}

// Runtime is a container engine. internal/docker implements Runtime by
// shelling out to the docker binary; another engine implements it another
// way.
//
// Runtime carries the container lifecycle, the shared project network every
// step joins, and the escape hatches (Exec, Save, NetworkConnect,
// Available) a caller needs to drive a container it created outside of Run
// - such as kind's own node containers. internal/docker exposes still more
// methods (NetworkGateway, ListByLabel, ...) that a caller who already
// knows the engine can use directly on the concrete type; they join Runtime
// only once something needs to call them without knowing the engine.
type Runtime interface {
	// Available reports whether the engine command runs and its daemon
	// answers. Available returns ErrUnavailable when it does not.
	Available(ctx context.Context) error

	// Run creates a container and returns the container ID.
	Run(ctx context.Context, spec RunSpec) (string, error)

	// Remove stops and deletes a container. Remove is idempotent: a
	// container that is absent is not an error.
	Remove(ctx context.Context, name string) error

	// Inspect reports the state of one container. Inspect returns
	// ErrNotFound when the container is absent.
	Inspect(ctx context.Context, name string) (Container, error)

	// Exec runs a command inside a container and returns its standard
	// output.
	Exec(ctx context.Context, container string, args ...string) (string, error)

	// ExecInput runs a command inside a container, with stdin feeding the
	// command, and returns its standard output.
	ExecInput(ctx context.Context, container string, stdin io.Reader, args ...string) (string, error)

	// Save streams a container image as a tar archive. The caller must
	// close the returned reader.
	Save(ctx context.Context, image string) (io.ReadCloser, error)

	// NetworkCreate creates the network that every step of a project
	// joins. The call succeeds when the network exists already.
	NetworkCreate(ctx context.Context, name string, labels map[string]string) error

	// NetworkRemove removes a project's network. A network that is absent
	// is not an error.
	NetworkRemove(ctx context.Context, name string) error

	// NetworkConnect joins an existing container, such as a kind node, to
	// a network it was not created on.
	NetworkConnect(ctx context.Context, network, container string) error
}
