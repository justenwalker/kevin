// Package route registers one or more subdomains of the environment domain
// as HTTP routes into an address - a host-reachable address already
// dialable directly, such as one of a builtin:container step's host_80
// style outputs, or a target behind a relay, such as a Kubernetes
// Service inside a builtin:kind cluster, reached through a SOCKS5 relay
// address such as a builtin:kind step's relay_addr output.
// This is the one mechanism for putting a step on the environment
// domain, whatever kind of step produced the address.
//
// An entry with external set skips the domain suffix and uses its host
// exactly as given instead - a real-world hostname, such as
// "s3.amazonaws.com", rather than a subdomain of the environment. This
// lets a step intercept traffic meant for a real service and redirect it
// to a local stand-in.
//
// A route step deploys nothing itself: it only tells the kevin proxy how
// to dial each host. It has no Down.
package route

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// config is the decoded with block of one step.
type config struct {
	Relay  string        `json:"relay"`
	Routes []routeConfig `json:"routes"`
}

// routeConfig is one entry of the with block's routes list.
type routeConfig struct {
	Host     string `json:"host"`
	Address  string `json:"address"`
	TLS      bool   `json:"tls"`
	External bool   `json:"external"`
}

// Step is the route step.
type Step struct{}

// New returns the route step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of a route step.
func (Step) Schema() []byte { return schema }

// Kind reports that a route step is apply-only: it registers routes but
// owns no lifecycle of its own.
func (Step) Kind() plugin.StepKind { return plugin.StepKindAction }

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a route step is idempotent. The proxy's route
// table replaces an earlier route for the same host rather than growing
// one.
func (Step) Idempotent() bool { return true }

// Up registers one route per with-block entry. When relay is set, each
// route dials it and issues a SOCKS5 CONNECT to the entry's address;
// otherwise the entry's address must already be something the proxy
// process can dial directly.
func (Step) Up(_ context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	routes := make([]plugin.Route, 0, len(cfg.Routes))
	details := make([]plugin.Detail, 0, len(cfg.Routes))
	for _, e := range cfg.Routes {
		host := e.Host + "." + req.Env.Domain
		if e.External {
			host = e.Host
		}
		upstream := e.Address
		if cfg.Relay != "" {
			upstream = fmt.Sprintf("socks5://%s/%s", cfg.Relay, e.Address)
		}
		r := plugin.Route{Host: host, Upstream: upstream, TLS: e.TLS}
		routes = append(routes, r)
		details = append(details, r.Detail())
		out.Log("stdout", "routing "+host+" to "+e.Address)
	}

	return &plugin.Result{Routes: routes, Details: details}, nil
}

// decode parses the with-block JSON into a config.
func decode(data []byte) (config, error) {
	var cfg config
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("route: decode config: %w", err)
	}
	return cfg, nil
}
