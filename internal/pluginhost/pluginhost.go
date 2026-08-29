// Package pluginhost starts plugin binaries.
// Each plugin process stays alive for the life of the supervisor.
package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/justenwalker/kevin/internal/logging"
	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/plugin"
	"github.com/justenwalker/kevin/protos/pb"
)

var log = logging.New("pluginhost")

// Spec describes how to start a plugin.
type Spec struct {
	// Cmd is the plugin binary. Dir resolves a relative path.
	Cmd string

	// Args are passed to the binary.
	Args []string

	// Env holds extra environment variables, on top of the environment of the
	// supervisor.
	Env map[string]string

	// Dir is the project directory. Dir resolves Cmd, and Dir is the working
	// directory of the plugin process.
	Dir string
}

// Client is a running plugin process. A Client is safe for concurrent use.
type Client struct {
	name   string
	client *goplugin.Client
	api    pb.PluginClient
	closer io.Closer
}

// Launch starts the plugin binary and connects to the process. The process runs until a call to Close.
func Launch(ctx context.Context, name string, spec Spec) (*Client, error) {
	cmdPath := spec.Cmd
	if !filepath.IsAbs(cmdPath) && (filepath.Base(cmdPath) != cmdPath) {
		cmdPath = filepath.Join(spec.Dir, cmdPath)
	}

	// Not using CommandContext deliberately.
	// go-plugin owns the process lifetime. An interrupt cancels ctx.
	// The supervisor still needs the plugin alive to call Down on the steps.
	//nolint:gosec,noctx // launching a configured plugin binary is the whole point
	cmd := exec.Command(cmdPath, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	logger, closer := hclogToSlog(log.Ctx(ctx).With("plugin", name))
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  plugin.Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{plugin.ProtocolVersion: {plugin.Name: &plugin.GRPCPlugin{}}},
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		AutoMTLS:         true,
		Logger:           logger,
	})

	cleanup := func() {
		client.Kill()
		_ = closer.Close()
	}

	rpc, err := client.Client()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("pluginhost: connect to plugin %q: %w", name, err)
	}

	raw, err := rpc.Dispense(plugin.Name)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("pluginhost: dispense plugin %q: %w", name, err)
	}

	api, ok := raw.(pb.PluginClient)
	if !ok {
		cleanup()
		return nil, fmt.Errorf("pluginhost: plugin %q returned %T: %w", name, raw, ErrNameMismatch)
	}

	return &Client{
		name:   name,
		client: client,
		api:    api,
		closer: closer,
	}, nil
}

// StepInfo is what a plugin reports about one step type it offers.
type StepInfo struct {
	// Name identifies the step type.
	Name string

	// Schema constrains the with block of this step type.
	Schema []byte

	// Export reports whether this step type implements Export.
	Export bool

	// Down reports whether this step type implements Down.
	Down bool

	// Kind reports what this step type is.
	Kind pb.StepKind

	// Idempotent reports whether this step type is safe to call Up on
	// again - safe to include in a cascading rerun triggered by a
	// different step.
	Idempotent bool
}

// Info is what a plugin reports about itself.
type Info struct {
	// Name identifies the plugin.
	Name string

	// Version appears in diagnostics only.
	Version string

	// Schema constrains the config block of the plugin. It is empty when the
	// plugin takes no configuration.
	Schema []byte

	// Steps are the step types the plugin offers.
	Steps []StepInfo

	// Icon is a small PNG image that represents the plugin, or nil when
	// it gave none.
	Icon []byte
}

// Info reports the plugin identity, the configuration schema, and the step
// types the plugin offers.
func (c *Client) Info(ctx context.Context) (Info, error) {
	resp, err := c.api.Info(ctx, &pb.InfoRequest{})
	if err != nil {
		return Info{}, fmt.Errorf("pluginhost: %q Info: %w", c.name, wrapRPCErr(err))
	}
	steps := make([]StepInfo, 0, len(resp.GetSteps()))
	for _, st := range resp.GetSteps() {
		steps = append(steps, StepInfo{
			Name:       st.GetName(),
			Schema:     st.GetCueSchema(),
			Export:     st.GetExport(),
			Down:       st.GetDown(),
			Kind:       st.GetKind(),
			Idempotent: st.GetIdempotent(),
		})
	}
	info := Info{
		Name:    resp.GetName(),
		Version: resp.GetVersion(),
		Schema:  resp.GetConfigSchema(),
		Steps:   steps,
		Icon:    resp.GetIcon(),
	}
	if info.Name != c.name {
		return info, fmt.Errorf("pluginhost: configured as %q but identifies as %q: %w", c.name, info.Name, ErrNameMismatch)
	}
	return info, nil
}

// Configure sends the config block of the plugin. Configure must run once,
// before any step of this plugin runs.
func (c *Client) Configure(ctx context.Context, config []byte, env *pb.Environment) error {
	if _, err := c.api.Configure(ctx, &pb.ConfigureRequest{Config: config, Env: env}); err != nil {
		return fmt.Errorf("pluginhost: %q Configure: %w", c.name, wrapRPCErr(err))
	}
	return nil
}

// Up creates a step. Up calls onEvent for every log line and every progress
// report before it returns.
func (c *Client) Up(ctx context.Context, req *pb.UpRequest, onEvent func(*pb.Event)) (*pb.Result, error) {
	stream, err := c.api.Up(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pluginhost: %q Up %q: %w", c.name, req.GetStep(), wrapRPCErr(err))
	}

	var result *pb.Result
	for {
		ev, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, fmt.Errorf("pluginhost: %q Up %q: %w", c.name, req.GetStep(), wrapRPCErr(recvErr))
		}
		if r := ev.GetResult(); r != nil {
			result = r
			continue
		}
		onEvent(ev)
	}

	if result == nil {
		return nil, fmt.Errorf("pluginhost: %q step %q: %w", c.name, req.GetStep(), ErrNoResult)
	}
	return result, nil
}

// Export asks a step how to reach what it created.
func (c *Client) Export(ctx context.Context, req *pb.ExportRequest) (*pb.ExportResponse, error) {
	resp, err := c.api.Export(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pluginhost: %q Export %q: %w", c.name, req.GetStep(), wrapRPCErr(err))
	}
	return resp, nil
}

// Down removes a step. Down calls onEvent for every log line and every
// progress report before it returns.
func (c *Client) Down(ctx context.Context, req *pb.DownRequest, onEvent func(*pb.Event)) error {
	stream, err := c.api.Down(ctx, req)
	if err != nil {
		return fmt.Errorf("pluginhost: %q Down %q: %w", c.name, req.GetStep(), wrapRPCErr(err))
	}
	for {
		ev, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return nil
		}
		if recvErr != nil {
			return fmt.Errorf("pluginhost: %q Down %q: %w", c.name, req.GetStep(), wrapRPCErr(recvErr))
		}
		onEvent(ev)
	}
}

// Close stops the plugin process.
func (c *Client) Close() {
	c.client.Kill()
	_ = c.closer.Close()
}

// wrapRPCErr converts an RPC error into a pluginhost error.
func wrapRPCErr(err error) error {
	if status.Code(err) == codes.Unavailable {
		return fmt.Errorf("%w: %w", ErrCrashed, err)
	}
	if msg := userMessage(err); msg != nil {
		return uerr.WrapText(err, msg.GetKey(), msg.GetArgs()...)
	}
	return err
}

// userMessage returns the [pb.UserMessage] detail on err's gRPC status, or
// nil if err carries none - either because it isn't a status error, or
// because the plugin never wrapped it with a human-facing message.
func userMessage(err error) *pb.UserMessage {
	st, ok := status.FromError(err)
	if !ok {
		return nil
	}
	for _, d := range st.Details() {
		if msg, ok := d.(*pb.UserMessage); ok {
			return msg
		}
	}
	return nil
}
