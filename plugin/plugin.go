// Package plugin is the SDK for kevin plugins.
//
// A plugin is a standalone binary. The binary builds a [Plugin] value and
// gives it to [Serve]. Serve speaks the kevin plugin protocol over gRPC on
// stdio.
//
//	func main() {
//		plugin.Serve(plugin.Plugin{
//			Name:  "acme",
//			Steps: map[string]plugin.Step{"widget": widgetStep{}},
//		})
//	}
package plugin

import (
	"context"
	"strings"

	goplugin "github.com/hashicorp/go-plugin"
)

// ProtocolVersion is the version of the wire protocol. A supervisor refuses a
// plugin that reports a different version.
const ProtocolVersion = 1

// Handshake is the magic that the supervisor and the plugin exchange. A
// binary that a user runs directly prints a message and exits.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ProtocolVersion,
	MagicCookieKey:   "KEVIN_PLUGIN",
	MagicCookieValue: "0b0f5a3c-2a4f-4d9d-9a55-3f3b5f2f2f34",
}

// Name is the key that the plugin is dispensed under. There is exactly one.
const Name = "step"

// Plugin provides the implementation of one or more Steps.
type Plugin struct {
	// Name must match the key in the CUE plugins block.
	Name string

	// Version appears in diagnostics only.
	Version string

	// ConfigSchema constrains the config block of this plugin. It is empty
	// when the plugin takes no configuration.
	ConfigSchema []byte

	// Configure is used to configure the plugin initially.
	Configure func(ctx context.Context, config []byte, env Env) error

	// Steps is a map of step names to their implementations.
	Steps map[string]Step

	// Icon is a small PNG image that represents the provider, shown next
	// to its step types in the console. Optional; nil shows no icon.
	// Keep it small: 48x48 or less. The console only ever displays it at
	// a small fixed size regardless.
	Icon []byte
}

// Env holds the values that the supervisor gives to every step. Env is the
// same for every step in a session.
type Env struct {
	// Project names the environment. Use it as the prefix of every resource
	// that the plugin creates.
	Project string

	// Workspace is the absolute path of the .kevin state directory.
	Workspace string

	// Network is the shared network that all workloads join.
	Network string

	// Engine names the container runtime a plugin should use, such as
	// "docker" or "podman". Empty means "docker".
	Engine string

	// EngineConfig is the marshaled bytes of the config message for Engine,
	// such as pb.DockerEngineConfig. Engine says which message to
	// unmarshal into.
	EngineConfig []byte

	// CAPath is the host path to the kevin root CA certificate file. Empty
	// means no CA is available.
	CAPath string

	// HTTPProxyAddr is the host:port of the kevin HTTP(S) proxy.
	HTTPProxyAddr string

	// ConsoleAddr is the host:port of the web console.
	ConsoleAddr string

	// ProxyEnv holds the proxy variables to add to a workload.
	ProxyEnv map[string]string

	// Domain is the base name of the environment, such as "kevin.home". A
	// step serves <step>.<domain> through the proxy.
	Domain string

	// Relay is the address of the in-network relay. A workload uses it for
	// DNS. Relay is empty when the relay is disabled.
	Relay string

	// RelaySOCKS5Addr is the host-reachable address of the relay's SOCKS5
	// gateway. A step builds a "socks5://<addr>/<target>" upstream against
	// it to reach a docker-network address through a single host port
	// instead of a dedicated published port. Empty when the relay is
	// disabled.
	RelaySOCKS5Addr string

	// RelaySOCKS5UDPAddrs maps a container-side UDP relay port (decimal
	// string) to its host-reachable address - see
	// ExposedPort.RelayUDPAddrs. Empty when the relay is disabled.
	RelaySOCKS5UDPAddrs map[string]string

	// ProjectDir is the absolute path of the directory that holds kevin.cue.
	// A step resolves a relative with-block path against this.
	ProjectDir string

	// Scope is which DAG this step belongs to: "setup" or "env". A plugin
	// should carry it as the "kevin.scope" label alongside
	// "kevin.project"/"kevin.urn".
	Scope string
}

// UpRequest asks the plugin to create one step.
type UpRequest struct {
	// Step is the name of this step in the environment.
	Step string

	// Type is the step type that this step uses.
	Type string

	Env Env

	// Config is the with block of the step, in JSON form.
	Config []byte

	// Deps maps the name of each upstream step to the outputs of that step.
	Deps map[string]map[string]Value

	// Containers carries each needs-step's own reported containers -
	// ContainerInfo, not string Outputs - so a plugin can read a
	// dependency's container identity directly. Only a needs-step that
	// reported any appears here.
	Containers []StepContainers
}

// DownRequest asks the plugin to remove one step.
type DownRequest struct {
	Step string

	// Type is the step type that this step uses.
	Type string

	Env Env

	Config []byte

	// Outputs is what Up published for this step.
	Outputs map[string]Value
}

// ExportRequest asks a step how to reach what it created.
type ExportRequest struct {
	// Step is the name of this step in the environment.
	Step string

	// Type is the step type that this step uses.
	Type string

	Env Env

	// Config is the with block of the step, in JSON form.
	Config []byte
}

// ExportResult carries what a step exports: Out, structured outputs for
// another step's cross-scope needs, or a "commands:" entry's run, to
// consume - the same Value shape Outputs uses.
type ExportResult struct {
	Out map[string]Value

	// Containers are the containers this step manages - the same
	// information Result.Containers carries for Up, mirrored here so a
	// cross-scope ("setup.<name>") reference gets the same container
	// identity a same-scope one does.
	Containers []ContainerInfo
}

// ToolDef describes one MCP tool a step type offers.
type ToolDef struct {
	Name        string
	Description string

	// InputSchema is the tool's parameters, an "object" JSON Schema
	// document. It must not declare a "step" property - the supervisor
	// injects that one itself.
	InputSchema []byte
}

// ToolCallRequest asks a step to run one of its declared tools.
type ToolCallRequest struct {
	// Step is the name of this step in the environment.
	Step string

	// Type is the step type that this step uses.
	Type string

	Env Env

	// Config is the with block of the step, in JSON form.
	Config []byte

	// Deps maps the name of each upstream step to the outputs of that step.
	Deps map[string]map[string]Value

	// Tool is the name from one of this step type's ToolDef entries.
	Tool string

	// Arguments is the MCP call's own arguments, in JSON form.
	Arguments []byte
}

// ToolCallResult is what a tool call returns.
type ToolCallResult struct {
	// Content is JSON-marshaled and surfaced to the MCP client as
	// structured content.
	Content any

	IsError bool

	// ErrorMessage is shown to the MCP client when IsError is true.
	ErrorMessage string
}

// Route is a hostname that a step serves. A Route in a [Result] joins the
// routing table of the proxy for the rest of the session.
type Route struct {
	// Host is the hostname that clients use.
	Host string

	// Upstream is the address to forward to. The address must be reachable on
	// the docker network.
	Upstream string

	// TLS is true when the upstream itself speaks TLS.
	TLS bool

	// Intercept marks Host a real-world hostname being intercepted, rather
	// than a subdomain of the environment domain, and carries the ports a
	// client dials it on. Nil means Host is a subdomain of the environment
	// domain, not a real-world hostname.
	Intercept *RouteIntercept

	// Mode selects how a client's connection to this route is handled - see
	// RouteMode.
	Mode RouteMode
}

// RouteIntercept marks a Route for interception: present, it names Host a
// real-world hostname to intercept, rather than a subdomain of the
// environment domain.
type RouteIntercept struct {
	// Ports lists the ports a client dials Host on, for a workload's own
	// DNS to also resolve Host to the relay.
	Ports []int
}

// ExposedPort is a raw TCP or UDP endpoint that a step publishes, bypassing
// the HTTP proxy - for a service that doesn't speak HTTP, such as a
// database's wire protocol - either directly on the host or through the
// environment's relay.
type ExposedPort struct {
	Name string

	// Protocol is "tcp" or "udp" - the real wire protocol spoken to the
	// target, regardless of whether Relay routes it through the SOCKS5
	// gateway.
	Protocol string

	// Upstream is the host-reachable address when !Relay, such as
	// "127.0.0.1:54321", or a "socks5://<relay>/<target>" upstream when
	// Relay is true.
	Upstream string

	// HostPort pins the port of the engine's local forward. Ignored unless
	// Relay is true; zero lets the OS assign one.
	HostPort int

	// Relay is true when Upstream must be reached through the
	// environment's SOCKS5 relay instead of dialed directly.
	Relay bool

	// RelayUDPAddrs maps a container-side UDP relay port (decimal string)
	// to its host-reachable address. Set only when Relay and Protocol ==
	// "udp": the relay's ASSOCIATE reply names one of these container
	// ports, and the engine's local UDP forward looks it up here, since
	// the relay's own bind address is a docker-network address the host
	// can't dial directly.
	RelayUDPAddrs map[string]string
}

// Detail is one extra piece of information a step shows on its console
// card. Detail is purely descriptive: it drives no proxy or DAG behavior,
// unlike Route or ExposedPort.
type Detail struct {
	Label string
	Value Value

	// Copyable shows a copy-to-clipboard button next to Value.
	Copyable bool

	// Href, when set, renders Value as a link to this URL instead of plain
	// text.
	Href string
}

// Detail returns a card row for r: a copyable link to "https://"+r.Host,
// unless r is an intercept route or a wildcard host - neither names a
// single address a client could actually browse to, so Value renders as
// plain text instead. Append it to Result.Details to keep a route visible
// on the card, or build a Detail by hand for something different.
func (r Route) Detail() Detail {
	d := Detail{Value: String(r.Host), Copyable: true}
	if r.Intercept == nil && !strings.HasPrefix(r.Host, "*.") {
		d.Href = "https://" + r.Host
	}
	return d
}

// Detail returns a card row for e: a copyable "<protocol> <name>": value
// row, with " (relay)" appended to the label when the port is reached
// through the environment's relay instead of published directly. Append it
// to Result.Details to keep an exposed port visible on the card, or build a
// Detail by hand for something different.
func (e ExposedPort) Detail() Detail {
	label := e.Protocol + " " + e.Name
	if e.Relay {
		label += " (relay)"
	}
	return Detail{Label: label, Value: String(e.Upstream), Copyable: true}
}

// Result is what a successful Up publishes.
type Result struct {
	// Outputs are the values that the dependents of this step can read.
	Outputs map[string]Value

	// Routes are the hostnames that this step serves.
	Routes []Route

	// ExposedPorts are raw TCP or UDP endpoints that this step publishes
	// directly to the host.
	ExposedPorts []ExposedPort

	// EgressAllow lists the external hosts that this step can reach. The proxy
	// denies egress by default, thus a step that reaches the internet must
	// name every host.
	EgressAllow []string

	// Details are the rows this step shows on its console card - the only
	// channel that reaches the card; Route/ExposedPort are functional (proxy
	// routing, port publishing) and do not themselves auto-populate the
	// card.
	Details []Detail

	// Containers are the containers this step manages - empty for a step
	// with no container workload of its own. A container step reports
	// exactly one; a step managing several (a kind cluster's nodes)
	// reports one per container. The engine forwards these to the relay
	// so each container's egress can be transparently redirected there,
	// and hands a dependent step's own Containers to it directly on
	// UpRequest.Containers.
	Containers []ContainerInfo

	// Faults are the network impairments this step wants the relay to
	// apply - see NetworkFault.
	Faults []NetworkFault
}

// ContainerInfo is what a plugin reports about one container it manages -
// a builtin:container step's own container, or one node of a
// builtin:kind cluster. Nothing about this type is specific to any one
// plugin: a step that manages several containers just reports several
// entries.
type ContainerInfo struct {
	// ID is the container engine's own id.
	ID string

	// Name is the container engine's own name.
	Name string

	// NetnsPath is the host path of this container's network namespace,
	// such as "/proc/1234/ns/net" - empty if the container isn't running.
	NetnsPath string

	// ExcludeCIDRs lists destination CIDRs that must never be redirected
	// to the relay when capturing this namespace's egress - a Kubernetes
	// cluster's own pod and service subnets, so pod-to-pod and
	// pod-to-service traffic keeps working normally. A non-empty list is
	// also what tells the relay this namespace routes traffic for others
	// rather than only generating its own, so it captures what transits
	// the namespace instead of what originates in it.
	ExcludeCIDRs []string
}

// StepContainers is one needs-step's reported containers, aggregated by
// the engine into UpRequest.Containers - order matches that step's
// position in needs.
type StepContainers struct {
	// Step is the needs-step this container list belongs to - a
	// same-scope step name, or "setup.<name>" for a cross-scope
	// reference, the same string form needs itself already uses.
	Step string

	Containers []ContainerInfo
}

// NetworkFault is one netem impairment the relay should apply - see
// Result.Faults. Unlike ContainerInfo, this already carries a resolved
// NetnsPath: the plugin that produces it (builtin:fault) resolves its own
// target from UpRequest.Containers before returning it, so the engine does
// no fault-specific resolution of its own.
type NetworkFault struct {
	// ID identifies this fault for the relay's own bookkeeping, so a
	// later teardown can remove exactly what was applied.
	ID string

	// NetnsPath is the host path of the target network namespace.
	NetnsPath string

	// Interface is the network interface inside the namespace to impair.
	// Empty means the relay's own default ("eth0").
	Interface string

	// DelayMS and JitterMS are the fixed delay and its random jitter, in
	// milliseconds. 0 means no delay.
	DelayMS, JitterMS int32

	// LossPercent, CorruptPercent, DuplicatePercent, and ReorderPercent
	// are each a percentage, 0-100, of packets affected. 0 means none.
	LossPercent, CorruptPercent, DuplicatePercent, ReorderPercent float64

	// RateKbit caps the interface's throughput, in kilobits/second. 0
	// means no cap.
	RateKbit int32
}

// NetnsTarget is one network namespace the relay should capture, for a step
// that manages more than one - see Result.NetnsTargets.
type NetnsTarget struct {
	// ID identifies this network namespace for the relay's own bookkeeping
	// and logs, such as "<step>/<node>" for one of a kind cluster's nodes.
	ID string

	// NetnsPath is the host path of the network namespace, such as
	// "/var/run/docker/netns/1234abcd".
	NetnsPath string

	// ExcludeCIDRs lists destination CIDRs that must never be redirected to
	// the relay - a Kubernetes cluster's own pod and service subnets, so
	// pod-to-pod and pod-to-service traffic keeps working normally. A
	// non-empty list is also what tells the relay this namespace routes
	// traffic for others rather than only generating its own, so it
	// captures what transits the namespace instead of what originates in
	// it.
	ExcludeCIDRs []string
}

// Emitter reports progress while a step runs. Everything that a Step emits
// reaches the supervisor at once.
type Emitter interface {
	// Log records one line. The stream value is "stdout" or "stderr".
	Log(stream, text string)

	// Progress reports advancement toward total. A total of 0 means that the
	// total is unknown.
	Progress(label string, current, total int64)
}

// Step is a step type, implemented by a plugin.
// A Step must be safe for concurrent use.
type Step interface {
	// Schema constrains the with block of this step type.
	// Return nil if no configuration is required.
	Schema() []byte

	// Kind classifies what this step type is.
	// StepKind is a classification of what a step does but has no impact on its behavior.
	Kind() StepKind

	// Up executes the step's up implementation.
	// Up will typically create a resource or perform an action.
	Up(ctx context.Context, req *UpRequest, out Emitter) (*Result, error)
}

// Downer is an interface that indicates a step has a tear-down implementation.
// When the system is shutting down, Down will be called for each step that has a Downer implementation.
type Downer interface {
	Down(ctx context.Context, req *DownRequest, out Emitter) error
}

// Exporter is an interface that indicates a step creates a resource that can be connected to.
// It reports structured values a "commands:" entry's run or a cross-scope needs reference can read.
type Exporter interface {
	Export(ctx context.Context, req *ExportRequest) (*ExportResult, error)
}

// IdempotentStep is an interface that indicates if a step is idempotent.
// Idempotent steps are safe to run multiple times without side effects.
type IdempotentStep interface {
	Idempotent() bool
}

// ToolProvider is an interface that indicates a step type offers one or
// more MCP tools, callable against a running step instance.
type ToolProvider interface {
	Tools() []ToolDef
	CallTool(ctx context.Context, req *ToolCallRequest) (*ToolCallResult, error)
}
