package config

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/justenwalker/kevin/internal/expr"
)

// Builtin is the plugin name that resolves to kevin's builtin step types.
const Builtin = "builtin"

// PluginSpec is one entry in the plugins block.
type PluginSpec struct {
	Cmd    string            `json:"cmd"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
	Config json.RawMessage   `json:"config"`

	OCI      string `json:"oci"`
	File     string `json:"file"`
	Checksum string `json:"checksum"`
	HTTP     string `json:"http"`

	// Signing is nil when the package requires no signature. schema.cue's
	// "signing" field unifies to JSON false (no signature required) or an
	// object naming a scheme - UnmarshalJSON turns that union into
	// nil-vs-populated here.
	Signing *SigningSpec `json:"-"`
}

// UnmarshalJSON decodes a PluginSpec, additionally turning the "signing"
// field's false/object union (see schema.cue) into a nil-vs-populated
// [PluginSpec.Signing].
func (p *PluginSpec) UnmarshalJSON(data []byte) error {
	type alias PluginSpec
	aux := struct {
		*alias

		Signing json.RawMessage `json:"signing"`
	}{alias: (*alias)(p)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return fmt.Errorf("config: decode plugin spec: %w", err)
	}
	if len(aux.Signing) == 0 || string(aux.Signing) == "false" {
		p.Signing = nil
		return nil
	}
	spec := &SigningSpec{}
	if err := json.Unmarshal(aux.Signing, spec); err != nil {
		return fmt.Errorf("config: decode plugin spec signing: %w", err)
	}
	p.Signing = spec
	return nil
}

// SigningScheme names a signature scheme a #Package's signing field can
// require (see schema.cue).
type SigningScheme string

// Signature schemes a #Package's signing field can require.
const (
	SigningSchemeMinisign SigningScheme = "minisign"
	SigningSchemeSigstore SigningScheme = "sigstore"
)

// SigningSpec is a #Package's signing field, decoded (see schema.cue's
// #Minisign/#Sigstore).
type SigningSpec struct {
	Scheme SigningScheme `json:"scheme"`

	// Identity and Issuer are set only for [SigningSchemeSigstore].
	Identity string `json:"identity"`
	Issuer   string `json:"issuer"`
}

// Step is one node of the DAG.
type Step struct {
	Uses  StepRef         `json:"uses"`
	Needs []Need          `json:"needs"`
	With  json.RawMessage `json:"with"`

	// Label is a friendly display name for the console. Empty means the
	// step's own key names it instead.
	Label string `json:"label"`

	// Timeout bounds the step's Up call. Zero means no limit.
	Timeout Timeout `json:"timeout"`

	// Watch lists the paths, relative to the project directory, whose
	// changes rerun the step.
	Watch []string `json:"watch"`

	tpl    *expr.Template
	tplErr error
}

// WithTemplate returns the parsed With block. A decoded step parsed it once;
// a Step built by hand parses it here. The returned template is shared and
// safe for concurrent use.
func (s Step) WithTemplate() (*expr.Template, error) {
	if s.tpl != nil || s.tplErr != nil {
		return s.tpl, s.tplErr
	}
	return expr.Parse(s.With)
}

// Command is one entry of the commands block, run on demand by name.
type Command struct {
	Needs []Need          `json:"needs"`
	Run   json.RawMessage `json:"run"`
	Cwd   string          `json:"cwd"`
	Label string          `json:"label"`
}

// PluginSchemas are the schemas that one plugin publishes.
type PluginSchemas struct {
	// Config constrains the config block of the plugin. It is empty when the
	// plugin takes no configuration.
	Config []byte

	// Steps constrains the with block of each step type, by step name.
	Steps map[string][]byte

	// Export reports, by step name, whether that step type implements
	// Export.
	Export map[string]bool
}

// Proxy configures the kevin proxy.
type Proxy struct {
	Listen      string      `json:"listen"`
	GatewayPort int         `json:"gateway_port"`
	Egress      ProxyEgress `json:"egress"`
}

// ProxyEgress configures which domains the proxy allows outbound traffic to.
type ProxyEgress struct {
	Allow       []string `json:"allow"`
	Deny        bool     `json:"deny"`
	Passthrough bool     `json:"passthrough"`
}

// Console configures the web console.
type Console struct {
	Listen string `json:"listen"`
}

// Engine configures the DAG engine that brings steps up and down.
type Engine struct {
	// MaxParallel caps how many steps run at once. 0 means no limit.
	MaxParallel int `json:"max_parallel"`
}

// Relay configures the in-network relay.
type Relay struct {
	Image     string         `json:"image"`
	Intercept RelayIntercept `json:"intercept"`
}

// RelayIntercept configures the address pools a registered intercept
// route's synthetic IP is allocated from.
type RelayIntercept struct {
	IPv4Range string `json:"ipv4_range"`
	IPv6Range string `json:"ipv6_range"`
}

// Config is a valid environment.
type Config struct {
	// Dir is the project directory that the environment came from.
	Dir string `json:"-"`

	Project string                `json:"project"`
	Domain  string                `json:"domain"`
	Plugins map[string]PluginSpec `json:"plugins"`

	// Name is the resolved, slugified environment name Load was given
	// (see [Load]), "" for the unnamed environment.
	Name string `json:"-"`

	// Setup steps persist across runs.
	Setup map[string]Step `json:"setup"`

	// Env steps are ephemeral.
	Env map[string]Step `json:"env"`

	// Commands run on demand with "kevin do <name>".
	Commands map[string]Command `json:"commands"`

	// Variables declares this environment's external inputs, by name.
	Variables map[string]Variable `json:"variables"`

	// VariableValues holds each declared variable's resolved value, once
	// [Config.ResolveVariables] has run - nil until then. A value can be
	// any JSON-representable type (string, int64, float64, bool, []any,
	// map[string]any, nil), per the variable's own declared type.
	VariableValues map[string]any `json:"-"`

	Proxy   Proxy   `json:"proxy"`
	Console Console `json:"console"`
	Relay   Relay   `json:"relay"`
	Engine  Engine  `json:"engine"`

	// groups holds each scope's step groups by their own bare name,
	// populated by [File.Config] - a group's member Steps already live in
	// Setup/Env at "<group>.<member>", groups carries only a group's own
	// metadata.
	groups map[Scope]map[string]Group
}

// Steps returns the steps of one scope.
func (c *Config) Steps(scope Scope) map[string]Step {
	if scope == ScopeSetup {
		return c.Setup
	}
	return c.Env
}

// Groups returns the step groups of one scope, by their own bare name -
// the same "setup"/"env" convention [Config.Steps] uses. A group's member
// Step already appears in [Config.Steps] at "<group>.<member>"; Groups
// exists for a group's own metadata (needs, outputs, label).
func (c *Config) Groups(scope Scope) map[string]Group {
	return c.groups[scope]
}
