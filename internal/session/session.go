// Package session is the DAG's step/run state - the entity model every
// console consumer (the web console, the terminal UI, the MCP server)
// reads.
package session

import "time"

// State is where a step is in its life.
type State string

// The states of a step.
const (
	Pending  State = "pending"
	Running  State = "running"
	Ready    State = "ready"
	Failed   State = "failed"
	Skipped  State = "skipped"
	Removing State = "removing"
	Removed  State = "removed"
)

// Step is one node of the DAG as it's shown to a consumer.
type Step struct {
	// Name is the step's identifier, from kevin.cue.
	Name string
	// Label is the display name the page shows. Empty means the page shows
	// Name instead.
	Label string
	// State is where the step is in its life.
	State State
	// Message is the state's detail text, e.g. an error message.
	Message string

	// Kind classifies what this step type is ("resource", "action",
	// "probe"), or "" when the plugin reported none.
	Kind string

	// Compact marks a gate-like step the sidebar renders as a single muted
	// line instead of a full card: a probe, or builtin:route.
	Compact bool

	// Provider names the plugin that backs this step, e.g. "builtin" -
	// the alt text for Icon.
	Provider string

	// Icon is the provider's PNG icon, or nil when the provider gave
	// none - or gave something that didn't look like a small PNG. A
	// consumer that renders it (the web console) encodes it itself.
	Icon []byte

	// Idempotent is true when this step's type is safe to call Up on
	// again - it may be swept into a cascading rerun of a step it depends
	// on, not just rerun directly.
	Idempotent bool

	// Needs are the names of the steps this one depends on, i.e. this
	// step's needs: list from kevin.cue - empty when it depends on
	// nothing. The sidebar draws a line per entry.
	Needs []string

	// Details are the rows this step's card shows.
	Details []Detail

	// Progress is the estimated fraction, in [0,1], of a running step's
	// duration that has elapsed. The zero value means no estimate exists -
	// the same value as "no history for this step" - and the page shows no
	// bar.
	Progress float64
}

// Detail is one row on a step's card.
type Detail struct {
	// Label is the row's caption.
	Label string
	// Value is the row's text.
	Value string
	// Copyable is true when the page shows a copy-to-clipboard button for
	// Value.
	Copyable bool
	// Href turns Value into a link, or "" for plain text.
	Href string
	// Sensitive is true when Value must never be logged or displayed in
	// full - the page shows a masked placeholder instead.
	Sensitive bool
}

// Line is one line of output from a step.
type Line struct {
	// Step is the name of the step that produced this line.
	Step string
	// Stream is the output stream this line came from, e.g. "stdout" or
	// "stderr".
	Stream string
	// Text is the line's content.
	Text string
}

// Request is one request that passed through the proxy.
type Request struct {
	// Time is when the proxy finished the request. The caller sets it -
	// Record does not stamp it.
	Time time.Time `json:"time"`

	// Method is the request's HTTP method.
	Method string `json:"method"`
	// Host is the request's target host.
	Host string `json:"host"`
	// Path is the request's target path.
	Path string `json:"path"`
	// Status is the response's HTTP status code.
	Status int `json:"status"`
	// Millis is how long the request took, in milliseconds.
	Millis int64 `json:"millis"`

	// Routed is true when the request reached a workload rather than the
	// internet.
	Routed bool `json:"routed"`

	// Denied is true when the proxy blocked the request instead of forwarding
	// it.
	Denied bool `json:"denied"`
}

// View is a snapshot of one session's step state.
type View struct {
	// Steps are the DAG's steps, in the order they were added.
	Steps []Step
	// Logs is the "All" tab's tail of output lines, across every step.
	Logs []Line
	// StepLogs holds each step's own log tail, by step name.
	StepLogs map[string][]Line
	// Requests is the Proxy tab's tail of requests, newest first.
	Requests []Request
	// ProxyAddr is where the proxy listens, or "" when it hasn't been set.
	ProxyAddr string
}
