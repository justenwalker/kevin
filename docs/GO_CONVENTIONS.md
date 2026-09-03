# Go conventions

This is the house style for `.go` files in this repository: the rules `golangci-lint` can't check, because they're judgment calls, not syntax. `.golangci.yaml` enables every linter by default and disables a handful deliberately; several of those disables are exactly the reason a rule below exists (noted per rule). Read `.golangci.yaml`'s own comments alongside this file; together they're the full picture.

Each rule has an ID (`GO-###`) so a review comment or a commit can point at one directly. These are cross-package conventions: how any `.go` file in this repo is written, not a specific subsystem's design. That belongs in `ARCHITECTURE.md` instead.

## GO-001: Sentinel errors are `type Error string`, not `errors.New`

Any error a caller might check with `errors.Is` is a named constant of a package-local `Error` type, not a `var` built from `errors.New` or a bare `fmt.Errorf`. `.golangci.yaml` disables `err113` specifically because this is the house style, not a violation of it.

**DO:**
```go
// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrNotFound reports that a container, an image, or a network is absent.
const ErrNotFound = Error("docker: no such object")
```

**DO NOT:**
```go
var ErrNotFound = errors.New("no such object")

func Inspect(name string) error {
	// errors.New gives the caller nothing to check against, and it can't
	// be a const, so it invites a second, subtly different copy elsewhere.
	if !exists(name) {
		return fmt.Errorf("no such object: %s", name)
	}
	return nil
}
```

## GO-002: An error message starts with the producing package's name, and each wrapped layer adds only its own context

`.golangci.yaml` scopes `wrapcheck` via `ignore-package-globs` to only police genuinely external errors, because wrapping within this module is deliberate, not automatic: every returned error is expected to carry a `pkg: what was happening` prefix, and a caller that wraps it again adds its *own* prefix rather than restating what the inner error already said.

**DO:**
```go
// package docker
func (c *Client) Remove(ctx context.Context, name string) error {
	if _, err := run(ctx, nil, "rm", "--force", "--volumes", name); err != nil {
		return fmt.Errorf("docker: remove %q: %w", name, err)
	}
	return nil
}
```

**DO NOT:**
```go
func (c *Client) Remove(ctx context.Context, name string) error {
	if _, err := run(ctx, nil, "rm", "--force", "--volumes", name); err != nil {
		// No package name, capitalized, and a caller who wraps this again
		// has no consistent prefix to strip or match against.
		return fmt.Errorf("Failed to remove container: %v", err)
	}
	return nil
}
```

## GO-003: A concrete type returned from a constructor is exported, backed by a conformance assertion

Concrete types should be exported. If they conform to an interface, we should pair it with a compile-time assertion. Returning an Interface type is something that should be done sparingly, usually when the package is the sole implementer of the interface (it is a sealed type).

**DO:**
```go
// Step is the kind step.
type Step struct{}

// New returns the kind step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}
```

**DO NOT:**
```go
// kindStep is unexported; every external caller can use it only through
// plugin.Step, so exporting Step at all bought nothing over just returning
// plugin.Step in the first place.
type kindStep struct{}

func New() kindStep { return kindStep{} }
```

A constructor returns a pointer when the type carries state the rest of the program shares and mutates (`dag.New` returns `*Graph`); it returns a value when the type is a stateless, zero-size step implementation (`kind.New` returns `Step`, as above). The constructor itself is named `New` for plain allocation; a constructor that does I/O or another side effect as part of construction is named for that action instead (`relay.Start`, `config.Load`) so the verb in the name matches what actually happens before the value exists.

## GO-004: An interface is defined by the package that calls through it, not by the package that implements it

An interface couples every caller to wherever it's declared. Declare it next to the implementation and every caller that wants a second implementation, or just a mock, has to import a package whose real job is something else entirely. Declare it in the consumer instead, and the producer satisfies it implicitly; Go needs no `implements` keyword for that. GO-003's sealed-type exception still applies: if a package is deliberately the sole implementer, returning its interface from that same package is fine.

**DO:**
```go
// package cri, the consumer: it drives a container engine generically and
// doesn't care which one.
type Runtime interface {
	Run(ctx context.Context, spec RunSpec) (string, error)
	Remove(ctx context.Context, name string) error
}
```
```go
// package docker, the producer. It implements Runtime's methods without
// ever declaring the interface; the assertion below is only a compile-time
// check, not a dependency Runtime's definition needs.
var _ cri.Runtime = Client{}

func (c Client) Remove(ctx context.Context, name string) error { ... }
```

**DO NOT:**
```go
// package docker
// Declaring Runtime here forces every caller that wants to swap engines, or
// mock one, to import docker just for the type, coupling them to a
// package whose real job is shelling out to the docker binary.
type Runtime interface {
	Run(ctx context.Context, spec RunSpec) (string, error)
	Remove(ctx context.Context, name string) error
}

type Client struct{}

func (c Client) Run(ctx context.Context, spec RunSpec) (string, error) { ... }
```

## GO-005: A helper lives right after the function that calls it

A helper reads better next to its one caller than sorted into an exported/unexported block or dumped at the end of the file alphabetically.

**DO:**
```go
func runArgs(spec RunSpec) []string {
	...
	args = append(args, labelArgs(spec.Labels)...)
	...
}

func labelArgs(labels map[string]string) []string { ... }

func sortedKeys(m map[string]string) []string { ... }

func Run(ctx context.Context, spec RunSpec) (string, error) {
	out, err := run(ctx, nil, runArgs(spec)...)
	...
}
```

**DO NOT:**
```go
// A reader hits Run first, then has to jump to the bottom of the file (or
// to a separate helpers.go) to find out what runArgs and labelArgs
// actually do, in whatever order they happened to be added.
func Run(ctx context.Context, spec RunSpec) (string, error) { ... }
func Remove(ctx context.Context, name string) error { ... }
func Inspect(ctx context.Context, name string) (Container, error) { ... }

// ... 200 lines later ...
func runArgs(spec RunSpec) []string { ... }
func labelArgs(labels map[string]string) []string { ... }
func sortedKeys(m map[string]string) []string { ... }
```

## GO-006: A doc comment says what/how; an inline comment says why

One Go proverb states: "Documentation is for users." However, inline comments are for developers.

### GoDoc Comments

A documentation comment should be made on every exported function, type, and variable. The comment explains WHAT it is and HOW to use it.

Go doc comments always start with the name of the type, explaining briefly WHAT it is. Then it is followed by HOW to use it correctly.

- Where it is unclear, arguments should be documented, especially if there are constraints on the arguments.
- The return values should also be documented, so the user knows what they are getting by calling the function.
- Errors that it can return should also be documented, as these are part of the API.
- Invariants can be called out in the doc comment.
- A type whose constructor can be skipped says so: whether `var t T`/`T{}` is ready to use as-is, or must go through `New`/`NewXxx` first. State it in the same wording every time ("The zero value is ready to use." or "The zero value is not usable. Call [New].") so a reader never has to guess from the field list.

### In-line comments

In-line comments are for developers. They should be used sparingly. An in-line comment should only be used when the code is not self-explanatory, or there is an edge case that is not obvious. These comments explain why the code is written the way it is, often to explain a workaround or a gotcha.

### DO / DO NOT

**DO (doc comment, what/how):**
```go
// LoadOrGenerateIntermediate loads or generates a project's Certificate
// Authority. If the project CA is found and is signed by the root CA, it
// is returned. Otherwise, a new CA is generated and signed by the root CA.
func (m *Manager) LoadOrGenerateIntermediate() (*CA, error) { ... }
```

**DO NOT (doc comment, leaks why):**
```go
// LoadOrGenerateIntermediate exists so that each project gets its own
// short-lived authority instead of every project sharing the root
// directly, which would make revoking one project's trust affect all of
// them.
func (m *Manager) LoadOrGenerateIntermediate() (*CA, error) { ... }
```

**DO (doc comment, zero-value usability, stated plainly):**
```go
// Client runs docker commands. The zero value is ready to use; use [New]
// only when the caller needs to override a default.
type Client struct { ... }
```
```go
// Logger builds a logger tagged with a package name. The zero value is not
// usable. Call [New].
type Logger struct { ... }
```

**DO (inline comment, why):**
```go
// A cluster of this name may survive a crash. Remove it, so that Up is
// idempotent.
if err = provider.Delete(name, kubeconfig); err != nil {
	return nil, fmt.Errorf("kind: remove the previous cluster %q: %w", name, err)
}
```

**DO NOT (inline comment, restates what):**
```go
// Delete the cluster
if err = provider.Delete(name, kubeconfig); err != nil {
	return nil, fmt.Errorf("kind: remove the previous cluster %q: %w", name, err)
}
```

## GO-007: Tests use testify's `require`/`assert`, not raw `t.Fatalf`

Test assertions use testify's `require`/`assert`, not raw `t.Fatalf`. `require` stops the test immediately, and `assert` fails the test, but lets it continue (useful for collecting several test failures). `require` should be reserved for failures that would make the rest of the test meaningless.

**DO:**
```go
func TestInfoChecksTheName(t *testing.T) {
	client, err := pluginhost.Launch(t.Context(), "echo", pluginhost.Spec{Cmd: bin, Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	info, err := client.Info(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "echo", info.Name)
	assert.Contains(t, info.Steps, "echo")
}
```

**DO NOT:**
```go
func TestInfoChecksTheName(t *testing.T) {
	client, err := pluginhost.Launch(t.Context(), "echo", pluginhost.Spec{Cmd: bin, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	info, err := client.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "echo" {
		t.Fatalf("Name = %q, want echo", info.Name)
	}
}
```

## GO-008: One top-level `TestXxx` per feature; cases are table entries or `t.Run` subtests

A `TestXxx` function names the feature that is being tested, and not a scenario.

Each Feature test should have sub-tests that test the scenarios using `t.Run`.

Preferably, those tests should use table-driven subtests so that new tests are added without changing the test function.

Table fields follow the same shape everywhere in this repo: the loop variable is `tt`, the case label is `name string`, and an expected value is `want`/`wantX`. When a case expects a specific error, `wantErr` is typed `error` (checked with `require.ErrorIs` and an early return), not `bool`; `bool` is reserved for cases that can't name a sentinel at all.

**DO:**
```go
func TestWalk(t *testing.T) {
	tests := []struct {
		name string
		dag  DAG
		want []string
	}{
		{name: "self cycle reports no reachable steps", dag: selfCycleDAG(), want: nil},
		{name: "cycle with a clean tail still walks the tail", dag: mixedDAG(), want: []string{"tail"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Walk(tt.dag)
			require.Error(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRun(t *testing.T) {
	t.Run("test a specific Run scenario", func(t *testing.T) {
		//...
    })
}
```

**DO (typed `wantErr`, checked with `ErrorIs`):**
```go
func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Config
		wantErr error
	}{
		{name: "valid config", in: validYAML, want: Config{Name: "web"}},
		{name: "missing name", in: noNameYAML, wantErr: ErrMissingName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

**DO NOT:**
```go
// Same feature, split across top-level funcs, no shared setup, no single
// place a reader checks to see every case this feature covers.
func TestWalkSelfCycle(t *testing.T) { ... }
func TestWalkMixedCycleWithCleanTail(t *testing.T) { ... }
func TestRunWithACertainScenario(t *testing.T) { ... }
```

## GO-009: A package's sentinel errors live in their own `errors.go`

A package that declares GO-001-style sentinel errors puts the `Error` type and its `const` block in a dedicated `errors.go`, separate from the file containing the logic that returns them. A reader checking what a package can fail with reads one short file, not the whole package.

**DO:**
```go
// errors.go
package dag

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrCycle reports that the graph has no valid order.
	ErrCycle = Error("dag: graph contains a cycle")

	// ErrUnknownStep reports a dependency on a step that does not exist.
	ErrUnknownStep = Error("dag: unknown step")
)
```

**DO NOT:**
```go
// dag.go
package dag

// ErrCycle reports that the graph has no valid order.
//
// Declared here, a reader has to skim past Walk, Validate, and every other
// function in this file before finding out the package has a second
// sentinel, ErrUnknownStep, three hundred lines down.
type Error string

func (e Error) Error() string { return string(e) }

const ErrCycle = Error("dag: graph contains a cycle")

func Walk(g *Graph) ([]string, error) { ... }
```

## GO-010: A configurable constructor takes an `Options` struct, not functional options

Every configurable constructor in this repo takes one `Options` struct parameter. No constructor takes `...func(*T)`. An `Options` struct is visible in its entirety at the call site and in godoc; functional options hide the available knobs behind function calls the reader has to chase down individually.

**DO:**
```go
// package ca
type Options struct {
	RootLifetime         time.Duration
	IntermediateLifetime time.Duration
}

// NewManager creates a Manager for a project rooted at cwd.
func NewManager(cwd string, project string, opt Options) *Manager { ... }
```
```go
mgr := ca.NewManager(cwd, project, ca.Options{RootLifetime: 24 * time.Hour})
```

**DO NOT:**
```go
type Option func(*Manager)

func WithRootLifetime(d time.Duration) Option {
	return func(m *Manager) { m.rootLifetime = d }
}

// The available options are whatever WithXxx functions happen to exist in
// this package; godoc on Manager won't list them.
func NewManager(cwd, project string, opts ...Option) *Manager { ... }
```

## GO-011: `ctx context.Context` is always the first parameter

Every function that takes a context takes it first, with that exact name and type: never buried after other arguments, never renamed, never part of an `Options` struct (GO-010's `Options` holds configuration, not per-call cancellation).

**DO:**
```go
func (c Client) Remove(ctx context.Context, name string) error { ... }
```

**DO NOT:**
```go
// A reader skimming signatures expects ctx first; finding it last (or
// inside opts) means checking every call site instead of the signature.
func (c Client) Remove(name string, opts Options, ctx context.Context) error { ... }
```

## GO-012: A receiver name is a short abbreviation of the type, used consistently

A receiver is named with the type's first letter (or first letters, for a multi-word type), never `this`, `self`, or a spelled-out word, and once chosen, every method on that type uses the same name.

**DO:**
```go
func (p *Proxy) AddRoutes(routes ...Route) { ... }
func (p *Proxy) AllowEgress(hosts ...string) { ... }

func (g *Graph) Steps() []string { ... }
func (g *Graph) TopoSort() []string { ... }
```

**DO NOT:**
```go
// Same type, two different receiver names across its methods, and self
// instead of a short abbreviation.
func (self *Proxy) AddRoutes(routes ...Route) { ... }
func (proxy *Proxy) AllowEgress(hosts ...string) { ... }
```

## GO-013: An interface is passed by value, never by pointer

A pointer to an interface (`*Runtime`) is almost always a mistake: the interface value already holds a pointer-sized word to the underlying data, so `*Runtime` just adds a second layer of indirection to something that doesn't need one, and breaks the moment a caller assigns a different concrete type through it.

**DO:**
```go
func waitRunning(ctx context.Context, runtime cri.Runtime, name string, deadline time.Time, out plugin.Emitter) (cri.Container, error) { ... }
```

**DO NOT:**
```go
// A pointer to an interface. runtime is now a pointer to a pointer (-ish)
// to the concrete Docker client; callers that want to swap the
// implementation can already do that by assigning a new Runtime value.
func waitRunning(ctx context.Context, runtime *cri.Runtime, name string, deadline time.Time, out plugin.Emitter) (cri.Container, error) { ... }
```

## GO-014: A mutex is a named, unexported field, not embedded

A struct that needs a `sync.Mutex`/`sync.RWMutex` declares it as a plain field, never embedded. One mutex may guard several related fields; it doesn't need to be split one-per-field. Name it `mu` when the struct has only one, or `<field>Mu` per mutex when a struct has more than one, each guarding a different group of fields. Embedding promotes `Lock`/`Unlock` into the type's own exported method set, which lets a caller outside the package take the lock. This ruins the encapsulation the mutex exists to provide.

**DO (one mutex per independent group of fields, not one per field):**
```go
type Server struct {
	mu       sync.RWMutex // guards the fields below
	steps    map[string]StepState
	started  time.Time
	finished bool

	// clientsMu guards clients separately: broadcasting to clients happens
	// on a different, hotter path than a step-state update.
	clientsMu sync.Mutex
	clients   map[chan []byte]struct{}
}
```

**DO NOT:**
```go
// Embedding promotes Lock/Unlock onto Runner itself; any caller that
// imports this package can now call runner.Lock() directly, and the
// struct can no longer add a second, differently-scoped mutex without
// an ambiguous selector.
type Runner struct {
	sync.Mutex
	forwards []*portForward
}
```

## GO-015: An error is handled once: logged or returned, never both

A function that encounters an error either logs it (because nothing above it will see the error again) or returns/wraps it (because something above it will), never both. Logging and then returning the same error produces the same failure at every level of the call stack that also logs it.

**DO:**
```go
conn, crw, err := hj.Hijack()
if err != nil {
	// Nothing above this handler sees a returned error, so this is the
	// only place to record it.
	log.Ctx(r.Context()).Debug("hijack failed", "error", err)
	return
}
```

**DO NOT:**
```go
conn, crw, err := hj.Hijack()
if err != nil {
	// The caller that receives this error is going to log it again:
	// now the same failure appears twice in the logs at two different
	// severities with two different messages.
	log.Ctx(r.Context()).Error("hijack failed", "error", err)
	return fmt.Errorf("proxy: hijack: %w", err)
}
```

## GO-016: An exported type meant to be called concurrently says so in its doc comment

A type whose methods are called from multiple goroutines states that plainly, in the same phrasing every time: "A `<Type>` is safe for concurrent use." A caller shouldn't have to read the implementation to find out whether it needs its own lock around a value.

**DO:**
```go
// Proxy is the kevin proxy. A Proxy is safe for concurrent use.
type Proxy struct { ... }
```
```go
// Client is a running plugin process. A Client is safe for concurrent use.
type Client struct { ... }
```

**DO NOT:**
```go
// Proxy is the kevin proxy.
//
// A reader has no way to tell from this comment whether calling AddRoutes
// from one goroutine while Serve runs in another is safe, short of reading
// every method body for a mutex.
type Proxy struct { ... }
```

## GO-017: An integration test is a testify `suite.Suite`, gated by both the build tag and a runtime skip

The `integration` build tag alone isn't the gate: a suite also checks its real dependency (Docker, a reachable daemon, a buildable image) at runtime and calls `t.Skip` if it's unavailable, so `go test -tags integration ./...` degrades gracefully on a machine that can't satisfy it instead of failing outright.

**DO:**
```go
//go:build integration

package relay_test

// RelaySuite drives one relay container against a real docker daemon.
type RelaySuite struct {
	suite.Suite

	network string
	relay   *relay.Relay
}

func TestRelaySuite(t *testing.T) {
	suite.Run(t, new(RelaySuite))
}

// SetupSuite creates the shared network and starts the relay once for every
// test in the suite.
func (s *RelaySuite) SetupSuite() {
	t := s.T()
	if err := dockerClient.Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
	...
}
```

**DO NOT:**
```go
//go:build integration

// The build tag is the only gate. On a machine with the tag set but no
// Docker daemon running, every test in this file fails instead of
// skipping, and CI can't tell "broken" from "dependency absent."
func TestRelayAgainstDocker(t *testing.T) {
	network := createNetwork(t)
	...
}
```

## GO-018: A package is one file per concern, each with its own `_test.go`

A package's files are split by what they're responsible for, named after that thing, not grouped into one large file, and not split alphabetically or exported/unexported. Each file's tests live in the matching `_test.go`, not a single package-wide test file.

**DO:**
```
internal/proxy/
  certs.go        certs_test.go
  errors.go
  mitm.go         mitm_test.go
  proxy.go        proxy_test.go
  relay.go
  websocket.go    websocket_test.go
```

**DO NOT:**
```
internal/proxy/
  proxy.go        // certs, mitm, relay, and websocket logic all dumped
                  // into the file named after the package itself
  proxy_test.go   // every test for all of the above, in no particular order
```

## GO-019: A function returns a single struct plus `error`, not three or more values

A function that would return three or more values (not counting `error`)
returns a single struct plus `error` instead. Prefer a type-safe wrapper type
over a stringly-typed or list-based design for sensitive data. For
contention on shared state, prefer reject-on-contention (`TryLock`) over a
blocking lock unless blocking is explicitly required. Don't pre-format a
user-facing message with `fmt.Sprintf`; keep the template and its args
separate so they can still be localized.

**DO:**
```go
// UpResult is the outcome of bringing a step up.
type UpResult struct {
	Outputs      map[string]string
	EgressAllow  []string
	ReadyAt      time.Time
}

func (s Step) Up(ctx context.Context, req UpRequest) (UpResult, error) { ... }
```

**DO NOT:**
```go
// A fourth return value means every call site destructures four positional
// results, and a caller that only wants two of them still has to name all
// four.
func (s Step) Up(ctx context.Context, req UpRequest) (map[string]string, []string, time.Time, error) { ... }
```
