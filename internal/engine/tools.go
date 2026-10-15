package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
)

// run holds the state of one scope.
// toolRoute maps a namespaced MCP tool name back to the plugin, step
// type, and bare tool name a plugin declared it under.
type toolRoute struct {
	plugin string
	step   string
	tool   string
}

// collectTools builds the MCP tool list for every distinct step type
// referenced in steps that declares tools, namespaced
// "<plugin>_<type>_<tool>". Returns the tool list alongside the route
// each namespaced name resolves to.
func collectTools(ctx context.Context, steps map[string]config.Step, caps map[string]pluginhost.Info) ([]mcpserver.ToolDef, map[string]toolRoute) {
	routes := make(map[string]toolRoute)
	var defs []mcpserver.ToolDef
	for _, step := range steps {
		ref := step.Uses
		for _, st := range caps[ref.Plugin].Steps {
			if st.Name != ref.Step {
				continue
			}
			for _, t := range st.Tools {
				name := ref.Plugin + "_" + ref.Step + "_" + t.Name
				if _, ok := routes[name]; ok {
					continue
				}
				schema, mergeErr := mergeStepProperty(t.InputSchema, ref)
				if mergeErr != nil {
					log.Ctx(ctx).Warn("skipping mcp tool with an invalid input schema", "tool", name, "error", mergeErr)
					continue
				}
				routes[name] = toolRoute{plugin: ref.Plugin, step: ref.Step, tool: t.Name}
				defs = append(defs, mcpserver.ToolDef{Name: name, Description: t.Description, InputSchema: schema})
			}
		}
	}
	return defs, routes
}

// mergeStepProperty adds a required "step" string property to schema, a
// plugin-declared "object" JSON Schema document, naming the step
// instance a call to this tool targets.
func mergeStepProperty(schema []byte, ref config.StepRef) ([]byte, error) {
	doc := map[string]any{"type": "object"}
	if len(schema) > 0 {
		if err := json.Unmarshal(schema, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", ref, err)
		}
	}
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["step"] = map[string]any{
		"type":        "string",
		"description": fmt.Sprintf("the name of a %s step, from kevin.cue", ref),
	}
	doc["properties"] = props

	required, _ := doc["required"].([]any)
	doc["required"] = append(required, "step")

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return out, nil
}

// CallTool runs tool - a namespaced name from collectTools - against the
// step named name. It rejects a step whose actual type doesn't match the
// tool's declared owner.
func (r *run) CallTool(ctx context.Context, name, tool string, args json.RawMessage) (mcpserver.ToolResult, error) {
	route, ok := r.toolRoutes[tool]
	if !ok {
		return mcpserver.ToolResult{}, fmt.Errorf("engine: no such tool %q", tool)
	}
	step, ok := r.steps[name]
	if !ok {
		return mcpserver.ToolResult{}, fmt.Errorf("engine: no step named %q", name)
	}
	ref := step.Uses
	if ref.Plugin != route.plugin || ref.Step != route.step {
		return mcpserver.ToolResult{}, fmt.Errorf("engine: step %q is a %s step, not a %s:%s step", name, ref, route.plugin, route.step)
	}
	client, ok := r.plugins[ref.Plugin]
	if !ok {
		return mcpserver.ToolResult{}, fmt.Errorf("engine: plugin %q not loaded", ref.Plugin)
	}

	setupDeps, _, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	deps := r.sameScopeDeps(name)
	with, err := r.renderWith(name, step, deps, setupDeps)
	if err != nil {
		return mcpserver.ToolResult{}, err
	}

	resp, err := client.CallTool(ctx, &pb.ToolCallRequest{
		Step: name, Type: ref.Step, Env: r.env, Config: with,
		Deps: depsToProto(depsWithSetup(deps, setupDeps)),
		Tool: route.tool, Arguments: args,
	})
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("engine: call tool %s on %s: %w", tool, name, err)
	}

	var content any
	if len(resp.GetContent()) > 0 {
		if err := json.Unmarshal(resp.GetContent(), &content); err != nil {
			return mcpserver.ToolResult{}, fmt.Errorf("engine: tool %s on %s: decode content: %w", tool, name, err)
		}
	}
	return mcpserver.ToolResult{Content: content, IsError: resp.GetIsError(), Message: resp.GetErrorMessage()}, nil
}
