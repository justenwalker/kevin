package plugin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/protos/pb"
)

// Serve runs p as a kevin plugin. Serve blocks until the supervisor
// disconnects. Serve is the whole body of the main function of a plugin.
func Serve(p Plugin) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{
			ProtocolVersion: {Name: &GRPCPlugin{Plugin: p}},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
	})
}

// GRPCPlugin adapts a [Plugin] to go-plugin. A supervisor uses GRPCPlugin
// with a zero Plugin to get a client. A plugin sets Plugin to serve.
type GRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin

	// Plugin is the plugin implementation.
	Plugin Plugin
}

// GRPCServer registers the provider with the gRPC server of the plugin.
func (p *GRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterPluginServer(s, &server{provider: p.Plugin})
	return nil
}

// GRPCClient returns the client that the supervisor uses to call the plugin.
func (p *GRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return pb.NewPluginClient(c), nil
}

// server translates the gRPC surface into calls on a [Plugin].
type server struct {
	provider Plugin
}

func (s *server) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	names := slices.Sorted(maps.Keys(s.provider.Steps))
	steps := make([]*pb.StepType, 0, len(names))
	for _, name := range names {
		step := s.provider.Steps[name]
		_, exports := step.(Exporter)
		_, downs := step.(Downer)
		idempotentStep, _ := step.(IdempotentStep)
		steps = append(steps, &pb.StepType{
			Name:       name,
			CueSchema:  step.Schema(),
			Export:     exports,
			Down:       downs,
			Kind:       stepKindToProto(step.Kind()),
			Idempotent: idempotentStep != nil && idempotentStep.Idempotent(),
		})
	}
	return &pb.InfoResponse{
		Name:         s.provider.Name,
		Version:      s.provider.Version,
		ConfigSchema: s.provider.ConfigSchema,
		Steps:        steps,
		Icon:         s.provider.Icon,
	}, nil
}

func (s *server) Configure(ctx context.Context, req *pb.ConfigureRequest) (*pb.ConfigureResponse, error) {
	config := req.GetConfig()
	if s.provider.Configure == nil {
		if len(config) > 0 {
			return nil, fmt.Errorf("plugin: %q: %w", s.provider.Name, ErrConfigureNotSupported)
		}
		return &pb.ConfigureResponse{}, nil
	}
	if err := s.provider.Configure(ctx, config, envFromProto(req.GetEnv())); err != nil {
		return nil, withUserMessage(err)
	}
	return &pb.ConfigureResponse{}, nil
}

// withUserMessage returns err unchanged, unless err carries a human-facing
// message attached via [Wrap] - then it returns a gRPC status error that
// carries that message as a [pb.UserMessage] detail, so it survives the
// RPC boundary for the supervisor to reconstruct.
func withUserMessage(err error) error {
	var ue *uerr.Error
	if !errors.As(err, &ue) {
		return err
	}
	st, detailErr := status.New(codes.Unknown, err.Error()).
		WithDetails(&pb.UserMessage{Key: ue.Format(), Args: ue.Args()})
	if detailErr != nil {
		return err
	}
	return fmt.Errorf("plugin: %w", st.Err())
}

// step returns the step type that typ names. step returns
// [ErrUnknownStepType] naming every type that the provider offers, when typ
// names none of them.
func (s *server) step(typ string) (Step, error) { //nolint:ireturn // there is no single concrete type to return.
	step, ok := s.provider.Steps[typ]
	if !ok {
		names := slices.Sorted(maps.Keys(s.provider.Steps))
		return nil, fmt.Errorf("plugin: %q offers no step type %q, available: %s: %w",
			s.provider.Name, typ, strings.Join(names, ", "), ErrUnknownStepType)
	}
	return step, nil
}

func (s *server) Up(req *pb.UpRequest, stream grpc.ServerStreamingServer[pb.Event]) error {
	step, err := s.step(req.GetType())
	if err != nil {
		return err
	}

	out := &emitter{stream: stream}

	result, err := step.Up(stream.Context(), &UpRequest{
		Step:   req.GetStep(),
		Type:   req.GetType(),
		Env:    envFromProto(req.GetEnv()),
		Config: req.GetConfig(),
		Deps:   depsFromProto(req.GetDeps()),
	}, out)
	if err != nil {
		return withUserMessage(err)
	}
	if result == nil {
		result = &Result{}
	}

	routes := make([]*pb.Route, 0, len(result.Routes))
	for _, r := range result.Routes {
		routes = append(routes, &pb.Route{Host: r.Host, Upstream: r.Upstream, Tls: r.TLS})
	}

	exposedPorts := make([]*pb.ExposedPort, 0, len(result.ExposedPorts))
	for _, ep := range result.ExposedPorts {
		exposedPorts = append(exposedPorts, &pb.ExposedPort{Name: ep.Name, Protocol: ep.Protocol, Upstream: ep.Upstream})
	}

	details := make([]*pb.Detail, 0, len(result.Details))
	for _, d := range result.Details {
		details = append(details, &pb.Detail{Label: d.Label, Value: valueToProto(d.Value), Copyable: d.Copyable, Href: d.Href})
	}

	if err := stream.Send(&pb.Event{Event: &pb.Event_Result{Result: &pb.Result{
		Outputs:      &pb.Outputs{Values: outputsToProto(result.Outputs)},
		Routes:       routes,
		ExposedPorts: exposedPorts,
		EgressAllow:  result.EgressAllow,
		Details:      details,
	}}}); err != nil {
		return fmt.Errorf("plugin: send the result: %w", err)
	}
	return nil
}

func (s *server) Down(req *pb.DownRequest, stream grpc.ServerStreamingServer[pb.Event]) error {
	step, err := s.step(req.GetType())
	if err != nil {
		return err
	}

	downer, ok := step.(Downer)
	if !ok {
		// In practice, this should not be called by a well-behaved plugin that indicated it doesn't support Down.
		// However, this case is here for defensive purposes.
		return status.Errorf(codes.Unimplemented, "plugin: %q step type %q does not support down", s.provider.Name, req.GetType())
	}

	err = downer.Down(stream.Context(), &DownRequest{
		Step:    req.GetStep(),
		Type:    req.GetType(),
		Env:     envFromProto(req.GetEnv()),
		Config:  req.GetConfig(),
		Outputs: outputsFromProto(req.GetOutputs().GetValues()),
	}, &emitter{stream: stream})
	return withUserMessage(err)
}

func (s *server) Export(ctx context.Context, req *pb.ExportRequest) (*pb.ExportResponse, error) {
	step, err := s.step(req.GetType())
	if err != nil {
		return nil, err
	}

	exporter, ok := step.(Exporter)
	if !ok {
		// In practice, this should not be called by a well-behaved plugin that indicated it doesn't support Export.
		// However, this case is here for defensive purposes.
		return nil, status.Errorf(codes.Unimplemented, "plugin: %q step type %q does not support export", s.provider.Name, req.GetType())
	}

	result, err := exporter.Export(ctx, &ExportRequest{
		Step:   req.GetStep(),
		Type:   req.GetType(),
		Env:    envFromProto(req.GetEnv()),
		Config: req.GetConfig(),
	})
	if err != nil {
		return nil, withUserMessage(err)
	}
	if result == nil {
		result = &ExportResult{}
	}
	return &pb.ExportResponse{Env: result.Env, Out: &pb.Outputs{Values: outputsToProto(result.Out)}}, nil
}

func envFromProto(e *pb.Environment) Env {
	return Env{
		Project:       e.GetProject(),
		Workspace:     e.GetWorkspace(),
		Network:       e.GetNetwork(),
		Engine:        e.GetEngine(),
		EngineConfig:  e.GetEngineConfig(),
		CAPath:        e.GetCaPath(),
		HTTPProxyAddr: e.GetHttpProxyAddr(),
		ConsoleAddr:   e.GetConsoleAddr(),
		ProxyEnv:      e.GetProxyEnv(),
		Domain:        e.GetDomain(),
		Relay:         e.GetRelay(),
		ProjectDir:    e.GetProjectDir(),
		Scope:         e.GetScope(),
	}
}

func depsFromProto(deps map[string]*pb.Outputs) map[string]map[string]Value {
	if len(deps) == 0 {
		return nil
	}
	out := make(map[string]map[string]Value, len(deps))
	for name, o := range deps {
		out[name] = outputsFromProto(o.GetValues())
	}
	return out
}

// valueToProto lowers an SDK Value to its wire form.
func valueToProto(v Value) *pb.Value {
	return &pb.Value{Kind: &pb.Value_StringValue{StringValue: v.Reveal()}, Sensitive: v.IsSensitive()}
}

// valueFromProto lifts a wire Value into an SDK Value.
func valueFromProto(v *pb.Value) Value { //nolint:ireturn // String or Sensitive-wrapped-String, decided by the wire value's own sensitive flag
	var val Value = String(v.GetStringValue())
	if v.GetSensitive() {
		val = Sensitive{val}
	}
	return val
}

// outputsToProto lowers a map of SDK Values to their wire form.
func outputsToProto(m map[string]Value) map[string]*pb.Value {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]*pb.Value, len(m))
	for k, v := range m {
		out[k] = valueToProto(v)
	}
	return out
}

// outputsFromProto lifts a map of wire Values into SDK Values.
func outputsFromProto(m map[string]*pb.Value) map[string]Value {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]Value, len(m))
	for k, v := range m {
		out[k] = valueFromProto(v)
	}
	return out
}

// emitter sends log and progress events on the open stream. One goroutine at a
// time can call Send, and a Step needs no more than that.
type emitter struct {
	stream grpc.ServerStreamingServer[pb.Event]
}

func (e *emitter) Log(stream, text string) {
	_ = e.stream.Send(&pb.Event{Event: &pb.Event_Log{Log: &pb.LogLine{
		Stream:   stream,
		Text:     text,
		UnixNano: time.Now().UnixNano(),
	}}})
}

func (e *emitter) Progress(label string, current, total int64) {
	_ = e.stream.Send(&pb.Event{Event: &pb.Event_Progress{Progress: &pb.Progress{
		Label:   label,
		Current: current,
		Total:   total,
	}}})
}
