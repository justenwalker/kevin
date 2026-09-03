package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
)

// setupCrossScopeDeps resolves every "setup."-prefixed entry in step's
// needs list via that setup step's Export, keyed by the unprefixed
// name - the shape the "setup" CEL variable uses.
func setupCrossScopeDeps(ctx context.Context, cfg *config.Config, plugins map[string]*pluginhost.Client, caps map[string]pluginhost.Info, env *pb.Environment, step config.Step) (map[string]dag.Outputs, error) {
	var out map[string]dag.Outputs
	for _, dep := range step.Needs {
		setupName, ok := strings.CutPrefix(dep, "setup.")
		if !ok {
			continue
		}
		setupStep, ok := cfg.Setup[setupName]
		if !ok {
			return nil, fmt.Errorf("needs %q: no such step in scope \"setup\"", dep)
		}
		ref, err := config.ParseStepRef(setupStep.Uses)
		if err != nil {
			return nil, err
		}
		client, ok := plugins[ref.Plugin]
		if !ok {
			return nil, fmt.Errorf("plugin %q not loaded", ref.Plugin)
		}
		if !stepExports(caps[ref.Plugin], ref.Step) {
			return nil, fmt.Errorf("setup step %q (%s) does not implement export", setupName, ref)
		}
		with, err := expr.Render(setupStep.With, setupName, expr.Scopes{Project: ca.ProjectVars(cfg.Dir, cfg.Name)})
		if err != nil {
			return nil, fmt.Errorf("setup step %q: %w", setupName, err)
		}
		resp, err := client.Export(ctx, &pb.ExportRequest{
			Step: setupName, Type: ref.Step, Env: env, Config: with,
		})
		if err != nil {
			return nil, fmt.Errorf("export setup step %q: %w", setupName, err)
		}
		if out == nil {
			out = make(map[string]dag.Outputs)
		}
		out[setupName] = outputsFromProto(resp.GetOut())
	}
	return out, nil
}

// outputsFromProto lifts a step's proto outputs into the DAG's Outputs map.
func outputsFromProto(o *pb.Outputs) dag.Outputs {
	values := o.GetValues()
	if len(values) == 0 {
		return nil
	}
	out := make(dag.Outputs, len(values))
	for k, v := range values {
		out[k] = output.Value{String: v.GetStringValue(), Sensitive: v.GetSensitive()}
	}
	return out
}

// stepExports reports whether info's step type name implements Export.
func stepExports(info pluginhost.Info, name string) bool {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Export
		}
	}
	return false
}

// execWith execs target in place of the kevin process, inheriting its
// terminal.
func execWith(target []string) error {
	bin, err := exec.LookPath(target[0])
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	if err := syscall.Exec(bin, target, os.Environ()); err != nil { //nolint:gosec // the command is what the user asked to run
		return fmt.Errorf("do: exec %s: %w", bin, err)
	}
	return nil
}
