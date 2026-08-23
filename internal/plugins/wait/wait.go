// Package wait blocks a step's dependents until a check succeeds: a TCP
// dial, an HTTP(S) probe, a kubectl wait/rollout status, an arbitrary
// command retried until it exits zero, or a fixed duration sleep.
//
// A tcp check's address may be a plain host:port, or a
// "socks5://<relay>/<host:port>" URL - the form a builtin:kind step's
// expose entries publish as an "expose_<name>" output - to reach a service
// inside a kind cluster through its SOCKS5 relay.
package wait

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/justenwalker/kevin/internal/kubectlcmd"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// config is the decoded with block of one step.
type config struct {
	Timeout  string         `json:"timeout"`
	Interval string         `json:"interval"`
	TCP      *tcpConfig     `json:"tcp"`
	HTTP     *httpConfig    `json:"http"`
	Kubectl  *kubectlConfig `json:"kubectl"`
	Exec     *execConfig    `json:"exec"`
	Duration string         `json:"duration"`
}

type tcpConfig struct {
	Address string `json:"address"`
}

type httpConfig struct {
	URL    string `json:"url"`
	Method string `json:"method"`
	Status int    `json:"status"`
}

type kubectlConfig struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
	Namespace  string `json:"namespace"`
	Resource   string `json:"resource"`
	For        string `json:"for"`
	Rollout    bool   `json:"rollout"`
}

type execConfig struct {
	Command []string `json:"command"`
}

// Step is the wait step.
type Step struct{}

// New returns the wait step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of a wait step.
func (Step) Schema() []byte { return schema }

// Kind reports that a wait step is a probe: it creates no resource.
func (Step) Kind() plugin.StepKind { return plugin.StepKindProbe }

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a wait step is idempotent. Up only checks or
// sleeps; it creates nothing.
func (Step) Idempotent() bool { return true }

// Up runs the configured check, retrying until it succeeds or the timeout
// passes.
func (Step) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}
	if err = validate(cfg); err != nil {
		return nil, err
	}

	if cfg.Duration != "" {
		if err = sleep(ctx, out, cfg.Duration); err != nil {
			return nil, err
		}
		return &plugin.Result{}, nil
	}

	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("wait: timeout %q: %w", cfg.Timeout, err)
	}
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		return nil, fmt.Errorf("wait: interval %q: %w", cfg.Interval, err)
	}
	deadline := time.Now().Add(timeout)

	switch {
	case cfg.TCP != nil:
		err = retry(ctx, out, "tcp "+cfg.TCP.Address, deadline, interval, func(ctx context.Context) error {
			return dialTCP(ctx, cfg.TCP.Address)
		})
	case cfg.HTTP != nil:
		err = retry(ctx, out, cfg.HTTP.URL, deadline, interval, func(ctx context.Context) error {
			return probeHTTP(ctx, *cfg.HTTP)
		})
	case cfg.Kubectl != nil:
		err = retry(ctx, out, cfg.Kubectl.Resource, deadline, interval, func(ctx context.Context) error {
			return checkKubectl(ctx, *cfg.Kubectl, interval)
		})
	default:
		err = retry(ctx, out, strings.Join(cfg.Exec.Command, " "), deadline, interval, func(ctx context.Context) error {
			return runExec(ctx, cfg.Exec.Command)
		})
	}
	if err != nil {
		return nil, err
	}
	return &plugin.Result{}, nil
}

// validate reports ErrCheck unless exactly one of tcp, http, kubectl, exec,
// duration is set, and ErrKubectlMode when a kubectl check sets zero, or
// both, of for and rollout.
func validate(cfg config) error {
	n := 0
	for _, set := range []bool{cfg.TCP != nil, cfg.HTTP != nil, cfg.Kubectl != nil, cfg.Exec != nil, cfg.Duration != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return ErrCheck
	}
	if cfg.Kubectl != nil && (cfg.Kubectl.For != "") == cfg.Kubectl.Rollout {
		return ErrKubectlMode
	}
	return nil
}

// retry calls attempt every interval until it succeeds, the deadline
// passes, or ctx is done. Each attempt runs with its own interval-bounded
// context.
func retry(ctx context.Context, out plugin.Emitter, label string, deadline time.Time, interval time.Duration, attempt func(ctx context.Context) error) error {
	out.Progress("waiting for "+label, 0, 0)
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, interval)
		start := time.Now()
		attemptErr := attempt(attemptCtx)
		cancel()
		if attemptErr == nil {
			out.Log("stdout", label+" is ready")
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
		}
		if time.Now().After(deadline) {
			return plugin.Wrap(
				fmt.Errorf("wait: %s did not become ready: %w: %w", label, ErrTimeout, attemptErr),
				"timed out waiting for %s to become ready - check the step's logs, or raise timeout in kevin.cue", label)
		}
		if remaining := interval - time.Since(start); remaining > 0 {
			select {
			case <-time.After(remaining):
			case <-ctx.Done():
				return ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
			}
		}
	}
}

// sleep waits for d, or until ctx ends.
func sleep(ctx context.Context, out plugin.Emitter, d string) error {
	dur, err := time.ParseDuration(d)
	if err != nil {
		return fmt.Errorf("wait: duration %q: %w", d, err)
	}
	out.Progress("waiting "+dur.String(), 0, 0)
	select {
	case <-time.After(dur):
		return nil
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
	}
}

// dialTCP dials address, closing the connection on success. address is a
// plain host:port, or a "socks5://<relay>/<host:port>" URL to dial through
// a kind step's relay.
func dialTCP(ctx context.Context, address string) error {
	dialer, target, err := tcpDialer(address)
	if err != nil {
		return err
	}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return err //nolint:wrapcheck // the caller (retry) already prefixes the label
	}
	return conn.Close() //nolint:wrapcheck // the dial itself succeeded; a failed probe-close is a rare edge case
}

func tcpDialer(address string) (proxy.ContextDialer, string, error) { //nolint:ireturn // a plain dial and a SOCKS5 dial are genuinely different concrete types, same as registry.Lookup
	relay, target, ok := splitSOCKS5(address)
	if !ok {
		return &net.Dialer{}, address, nil
	}
	d, err := proxy.SOCKS5("tcp", relay, nil, proxy.Direct)
	if err != nil {
		return nil, "", fmt.Errorf("wait: socks5 dialer: %w", err)
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, "", errors.New("wait: socks5 dialer does not support DialContext")
	}
	return cd, target, nil
}

// splitSOCKS5 splits a "socks5://<relay>/<target>" address into its relay
// and target parts. ok is false when address carries no socks5:// prefix.
func splitSOCKS5(address string) (string, string, bool) {
	rest, ok := strings.CutPrefix(address, "socks5://")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, "/")
}

func probeHTTP(ctx context.Context, cfg httpConfig) error {
	req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("wait: http request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err //nolint:wrapcheck // the caller (retry) already prefixes the label
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != cfg.Status {
		return fmt.Errorf("%w: got %d, want %d", ErrStatus, resp.StatusCode, cfg.Status)
	}
	return nil
}

func checkKubectl(ctx context.Context, cfg kubectlConfig, timeout time.Duration) error {
	if cfg.Rollout {
		_, err := kubectlcmd.RolloutStatus(ctx, kubectlcmd.RolloutStatusSpec{
			Kubeconfig: cfg.Kubeconfig,
			Context:    cfg.Context,
			Namespace:  cfg.Namespace,
			Resource:   cfg.Resource,
			Timeout:    timeout,
		})
		return err
	}
	_, err := kubectlcmd.Wait(ctx, kubectlcmd.WaitSpec{
		Kubeconfig: cfg.Kubeconfig,
		Context:    cfg.Context,
		Namespace:  cfg.Namespace,
		Resource:   cfg.Resource,
		For:        cfg.For,
		Timeout:    timeout,
	})
	return err
}

func runExec(ctx context.Context, command []string) error {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err //nolint:wrapcheck // the caller (retry) already prefixes the label
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

// decode parses the with-block JSON into a config, applying schema.cue's
// defaults.
func decode(data []byte) (config, error) {
	// Repeat the defaults of schema.cue. A caller can pass an empty config,
	// and then CUE never applies them.
	cfg := config{Timeout: "60s", Interval: "1s"}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("wait: decode config: %w", err)
	}
	// Repeat schema.cue's per-block defaults too, for the same reason.
	if cfg.HTTP != nil {
		if cfg.HTTP.Method == "" {
			cfg.HTTP.Method = http.MethodGet
		}
		if cfg.HTTP.Status == 0 {
			cfg.HTTP.Status = http.StatusOK
		}
	}
	return cfg, nil
}
