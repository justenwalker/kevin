// Package step defines the domain types the engine exchanges with a plugin.
// internal/launcher converts them to and from the wire messages, so no
// other package needs the wire types for step traffic.
package step

import (
	"time"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
)

// Kind is what a step type is.
type Kind uint8

// Kinds a step type reports.
const (
	// KindUnspecified is a step type that reported no kind.
	KindUnspecified Kind = iota
	// KindResource creates something that outlives the call.
	KindResource
	// KindAction does work and leaves nothing behind.
	KindAction
	// KindProbe checks that something answers.
	KindProbe
)

// String returns the console label of k, or "" for an unspecified kind.
func (k Kind) String() string {
	switch k {
	case KindResource:
		return "resource"
	case KindAction:
		return "action"
	case KindProbe:
		return "probe"
	case KindUnspecified:
		return ""
	default:
		return ""
	}
}

// Env is the environment a plugin receives with every call.
type Env struct {
	Project             string
	Workspace           string
	Network             string
	CAPath              string
	HTTPProxyAddr       string
	ConsoleAddr         string
	ProxyEnv            map[string]string
	Domain              string
	Relay               string
	RelaySOCKS5Addr     string
	RelaySOCKS5UDPAddrs map[int]string
	ProjectDir          string
	Scope               config.Scope

	Engine Engine
}

// Engine is a container engine kevin can drive. The zero value is docker.
type Engine uint8

// Container engines.
const (
	EngineDocker Engine = iota
	EnginePodman
)

// String returns the engine's CLI name.
func (e Engine) String() string {
	if e == EnginePodman {
		return "podman"
	}
	return "docker"
}

// UpRequest asks a plugin to bring one step up.
type UpRequest struct {
	Step       string
	Type       string
	Env        Env
	Config     []byte
	Deps       map[string]output.Map
	Containers []StepContainers
}

// DownRequest asks a plugin to remove one step.
type DownRequest struct {
	Step    string
	Type    string
	Env     Env
	Config  []byte
	Outputs output.Map
}

// ExportRequest asks a plugin how to reach what a step created.
type ExportRequest struct {
	Step   string
	Type   string
	Env    Env
	Config []byte
}

// ExportResult is what a step's Export reports.
type ExportResult struct {
	Out        output.Map
	Containers []Container
}

// ToolCall asks a plugin to run one of its tools against a step.
type ToolCall struct {
	Step      string
	Type      string
	Env       Env
	Config    []byte
	Deps      map[string]output.Map
	Tool      string
	Arguments []byte
}

// ToolResult is what a tool call returns. Content is JSON.
type ToolResult struct {
	Content      []byte
	IsError      bool
	ErrorMessage string
}

// Result is what a step published when it came up.
type Result struct {
	Outputs      output.Map
	Routes       []Route
	EgressAllow  []string
	ExposedPorts []ExposedPort
	Details      []Detail
	Containers   []Container
	Faults       []Fault
}

// RouteMode is how the proxy handles traffic for a route.
type RouteMode uint8

// Route modes.
const (
	// RouteModeMITM terminates TLS in the proxy.
	RouteModeMITM RouteMode = iota
	// RouteModePassthrough tunnels TLS untouched.
	RouteModePassthrough
	// RouteModeRaw forwards the bytes of a non-HTTP connection.
	RouteModeRaw
)

// Route maps a host name to an upstream the host can dial.
type Route struct {
	Host      string
	Upstream  Upstream
	TLS       bool
	Intercept *Intercept
	Mode      RouteMode
}

// Intercept asks the relay to answer for a route's host inside the project
// network.
type Intercept struct {
	Ports []int
}

// Upstream is where a route or exposed port sends traffic. Addr is set, or
// Relay is, not both.
type Upstream struct {
	Addr  string
	Relay *RelayTarget
}

// RelayTarget is an address reached through the relay's SOCKS5 listener.
type RelayTarget struct {
	// Relay is the relay's SOCKS5 address.
	Relay string
	// Target is the host:port to reach inside the project network.
	Target string
}

// Dial returns the address form the proxy dials: Addr itself, or
// "socks5://<relay>/<target>" for a relay target.
func (u Upstream) Dial() string {
	if u.Relay != nil {
		return "socks5://" + u.Relay.Relay + "/" + u.Relay.Target
	}
	return u.Addr
}

// Protocol is the transport of an exposed port.
type Protocol uint8

// Transports.
const (
	TCP Protocol = iota
	UDP
)

// String returns "tcp" or "udp".
func (p Protocol) String() string {
	if p == UDP {
		return "udp"
	}
	return "tcp"
}

// ExposedPort is a port a step makes reachable from the host.
type ExposedPort struct {
	Name          string
	Protocol      Protocol
	Upstream      Upstream
	HostPort      int
	RelayUDPAddrs map[int]string
}

// Detail is one labeled value a step shows in the console.
type Detail struct {
	Label    string
	Value    output.Value
	Copyable bool
	Href     string
}

// Container is a container a step created.
type Container struct {
	ID   string
	Name string

	// NetnsPath is the container's network namespace, or "" when the relay
	// cannot capture its traffic.
	NetnsPath    string
	ExcludeCIDRs []string
}

// StepContainers is the containers one earlier step published.
type StepContainers struct {
	Scope      config.Scope
	Step       string
	Containers []Container
}

// Fault is a network fault the relay applies inside a network namespace.
type Fault struct {
	ID               string
	NetnsPath        string
	Interface        string
	DelayMS          int32
	JitterMS         int32
	LossPercent      float64
	CorruptPercent   float64
	DuplicatePercent float64
	ReorderPercent   float64
	RateKbit         int32
}

// Events receives what a step reports while Up or Down runs. It is called
// from one goroutine at a time.
type Events interface {
	// Log reports one line a step wrote to stream.
	Log(stream, text string, at time.Time)
	// Progress reports how far a step is through label's work.
	Progress(label string, current, total int64)
}
