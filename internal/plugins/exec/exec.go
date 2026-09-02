// Package exec runs a command line once on the host as a step's Up, and
// optionally a second command on Down for cleanup.
package exec

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// config is the decoded with block of one step.
type config struct {
	Up     execConfig  `json:"up"`
	Down   *execConfig `json:"down"`
	Proxy  bool        `json:"proxy"`
	Egress []string    `json:"egress"`
}

// execConfig is one #Exec block: up or down.
type execConfig struct {
	Command []string          `json:"command"`
	Cwd     string            `json:"cwd"`
	Env     map[string]string `json:"env"`
}

// Step is the exec step.
type Step struct{}

// New returns the exec step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of an exec step.
func (Step) Schema() []byte { return schema }

// Kind reports that an exec step performs an action.
func (Step) Kind() plugin.StepKind { return plugin.StepKindAction }

// Step must keep satisfying plugin.Downer.
var _ plugin.Downer = Step{}

// Up runs the configured command. Its stdout, trimmed, becomes the
// step's "stdout" output.
func (Step) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	stdout, err := runExec(ctx, cfg.Up, cfg.Proxy, req.Env, out)
	if err != nil {
		return nil, err
	}

	return &plugin.Result{
		Outputs:     plugin.StringMap(map[string]string{"stdout": stdout}),
		EgressAllow: cfg.Egress,
	}, nil
}

// Down runs the configured cleanup command. It does nothing when the with
// block sets no down.
func (Step) Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
	cfg, err := decode(req.Config)
	if err != nil {
		return err
	}
	if cfg.Down == nil {
		return nil
	}
	_, err = runExec(ctx, *cfg.Down, cfg.Proxy, req.Env, out)
	return err
}

// runExec runs one #Exec block and returns its trimmed stdout.
func runExec(ctx context.Context, execCfg execConfig, proxy bool, env plugin.Env, out plugin.Emitter) (string, error) {
	label := strings.Join(execCfg.Command, " ")
	out.Log("stdout", "running "+label)

	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, execCfg.Command[0], execCfg.Command[1:]...)
	cmd.Dir = workDir(execCfg.Cwd, env.ProjectDir)
	cmd.Env = buildEnv(execCfg, proxy, env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", plugin.Wrap(fmt.Errorf("exec: %s: %w", label, err),
				"%q failed - check the step's logs", label)
		}
		return "", plugin.Wrap(fmt.Errorf("exec: %s: %s: %w", label, msg, err),
			"%q failed: %s", label, msg)
	}

	out.Log("stdout", label+" done")
	return strings.TrimSpace(stdout.String()), nil
}

// workDir resolves cwd against projectDir when cwd is relative. An empty
// cwd runs the command in the project directory.
func workDir(cwd, projectDir string) string {
	if cwd == "" {
		return projectDir
	}
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Join(projectDir, cwd)
}

// buildEnv merges the host environment, the command's own env, and, when
// proxy is set, the proxy variables and CA trust. Step variables take
// precedence over proxy variables.
func buildEnv(execCfg execConfig, proxy bool, env plugin.Env) []string {
	merged := make(map[string]string, len(execCfg.Env))
	maps.Copy(merged, execCfg.Env)
	if proxy {
		addProxyDefaults(merged, env)
	}

	result := os.Environ()
	for k, v := range merged {
		result = append(result, k+"="+v)
	}
	return result
}

// addProxyDefaults sets the proxy env vars and CA trust in merged, unless
// the with block already set them itself.
func addProxyDefaults(merged map[string]string, env plugin.Env) {
	// env.HTTPProxyAddr, not env.ProxyEnv: ProxyEnv is the docker-network
	// gateway address a container reaches the proxy through, but this
	// command runs directly on the host.
	if env.HTTPProxyAddr != "" {
		proxyURL := "http://" + env.HTTPProxyAddr
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			setDefault(merged, k, proxyURL)
		}
	}
	if env.CAPath != "" {
		setDefault(merged, "SSL_CERT_FILE", env.CAPath)
	}
}

// setDefault sets m[key] to value unless key is already present.
func setDefault(m map[string]string, key, value string) {
	if _, ok := m[key]; !ok {
		m[key] = value
	}
}

// decode parses the with-block JSON into a config.
func decode(data []byte) (config, error) {
	var cfg config
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("exec: decode config: %w", err)
	}
	return cfg, nil
}
