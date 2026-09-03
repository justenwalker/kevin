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

// ExportResult carries what a step exports: Env, the environment variables
// an external command needs to reach it (what "kevin connect" uses), and
// Out, the same outputs in structured form for another step's cross-scope
// needs to consume - the same Value shape Outputs uses.
type ExportResult struct {
	Env map[string]string
	Out map[string]Value
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
}

// ExposedPort is a raw TCP or UDP endpoint that a step publishes directly to
// the host, bypassing the HTTP proxy - for a service that doesn't speak
// HTTP, such as a database's wire protocol.
type ExposedPort struct {
	Name string

	// Protocol is "tcp" or "udp".
	Protocol string

	// Upstream is the host-reachable address, such as "127.0.0.1:54321".
	Upstream string
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

// Detail returns a card row for r: a bare copyable link to
// "https://"+r.Host. Append it to Result.Details to keep a route visible
// on the card, or build a Detail by hand for something different.
func (r Route) Detail() Detail {
	return Detail{Value: String(r.Host), Href: "https://" + r.Host, Copyable: true}
}

// Detail returns a card row for e: a copyable "<protocol> <name>": value
// row. Append it to Result.Details to keep an exposed port visible on the
// card, or build a Detail by hand for something different.
func (e ExposedPort) Detail() Detail {
	return Detail{Label: e.Protocol + " " + e.Name, Value: String(e.Upstream), Copyable: true}
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
// It exposes environment variables that can be used to connect to the resource.
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
