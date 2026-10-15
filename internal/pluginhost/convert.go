package pluginhost

import (
	"time"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginwire"
	"github.com/justenwalker/kevin/internal/step"
	"github.com/justenwalker/kevin/protos/pb"
)

// envToProto lowers env to its wire form. Any engine name other than
// "podman" is docker, the zero value.
func envToProto(env step.Env) *pb.Environment {
	engine := pb.Engine_ENGINE_DOCKER
	if env.Engine == step.EnginePodman {
		engine = pb.Engine_ENGINE_PODMAN
	}
	return &pb.Environment{
		Project:             env.Project,
		Workspace:           env.Workspace,
		Network:             env.Network,
		CaPath:              env.CAPath,
		HttpProxyAddr:       env.HTTPProxyAddr,
		ConsoleAddr:         env.ConsoleAddr,
		ProxyEnv:            env.ProxyEnv,
		Domain:              env.Domain,
		Relay:               env.Relay,
		RelaySocks5Addr:     env.RelaySOCKS5Addr,
		RelaySocks5UdpAddrs: pluginwire.PortsToProto(env.RelaySOCKS5UDPAddrs),
		ProjectDir:          env.ProjectDir,
		Scope:               scopeToProto(env.Scope),
		Engine:              engine,
	}
}

// scopeToProto lowers s to its wire form. Any scope other than setup is env.
func scopeToProto(s config.Scope) pb.Scope {
	if s == config.ScopeSetup {
		return pb.Scope_SCOPE_SETUP
	}
	return pb.Scope_SCOPE_ENV
}

// depsToProto lowers each dependency's outputs to its wire form.
func depsToProto(deps map[string]output.Map) map[string]*pb.Outputs {
	if len(deps) == 0 {
		return nil
	}
	out := make(map[string]*pb.Outputs, len(deps))
	for name, values := range deps {
		out[name] = outputsToProto(values)
	}
	return out
}

// upRequestToProto lowers req to its wire form.
func upRequestToProto(req step.UpRequest) *pb.UpRequest {
	containers := make([]*pb.StepContainers, 0, len(req.Containers))
	for _, sc := range req.Containers {
		containers = append(containers, &pb.StepContainers{
			Scope:      scopeToProto(sc.Scope),
			Step:       sc.Step,
			Containers: containersToProto(sc.Containers),
		})
	}
	return &pb.UpRequest{
		Step:       req.Step,
		Type:       req.Type,
		Env:        envToProto(req.Env),
		Config:     req.Config,
		Deps:       depsToProto(req.Deps),
		Containers: containers,
	}
}

// downRequestToProto lowers req to its wire form.
func downRequestToProto(req step.DownRequest) *pb.DownRequest {
	return &pb.DownRequest{
		Step:    req.Step,
		Type:    req.Type,
		Env:     envToProto(req.Env),
		Config:  req.Config,
		Outputs: outputsToProto(req.Outputs),
	}
}

// exportRequestToProto lowers req to its wire form.
func exportRequestToProto(req step.ExportRequest) *pb.ExportRequest {
	return &pb.ExportRequest{
		Step:   req.Step,
		Type:   req.Type,
		Env:    envToProto(req.Env),
		Config: req.Config,
	}
}

// exportResultFromProto lifts a wire export response. A nil message is an
// empty result.
func exportResultFromProto(resp *pb.ExportResponse) step.ExportResult {
	return step.ExportResult{
		Out:        outputsFromProto(resp.GetOut()),
		Containers: containersFromProto(resp.GetContainers()),
	}
}

// toolCallToProto lowers call to its wire form.
func toolCallToProto(call step.ToolCall) *pb.ToolCallRequest {
	return &pb.ToolCallRequest{
		Step:      call.Step,
		Type:      call.Type,
		Env:       envToProto(call.Env),
		Config:    call.Config,
		Deps:      depsToProto(call.Deps),
		Tool:      call.Tool,
		Arguments: call.Arguments,
	}
}

// toolResultFromProto lifts a wire tool response. A nil message is an empty
// result.
func toolResultFromProto(resp *pb.ToolCallResponse) step.ToolResult {
	return step.ToolResult{
		Content:      resp.GetContent(),
		IsError:      resp.GetIsError(),
		ErrorMessage: resp.GetErrorMessage(),
	}
}

// resultFromProto lifts a wire step result. A nil message is an empty
// result.
func resultFromProto(res *pb.Result) step.Result {
	out := step.Result{
		Outputs:     outputsFromProto(res.GetOutputs()),
		EgressAllow: res.GetEgressAllow(),
		Containers:  containersFromProto(res.GetContainers()),
	}
	for _, r := range res.GetRoutes() {
		out.Routes = append(out.Routes, routeFromProto(r))
	}
	for _, ep := range res.GetExposedPorts() {
		out.ExposedPorts = append(out.ExposedPorts, exposedPortFromProto(ep))
	}
	for _, d := range res.GetDetails() {
		out.Details = append(out.Details, step.Detail{
			Label:    d.GetLabel(),
			Value:    output.Value{String: d.GetValue().GetStringValue(), Sensitive: d.GetValue().GetSensitive()},
			Copyable: d.GetCopyable(),
			Href:     d.GetHref(),
		})
	}
	for _, f := range res.GetFaults() {
		out.Faults = append(out.Faults, faultFromProto(f))
	}
	return out
}

// routeFromProto lifts a wire Route.
func routeFromProto(r *pb.Route) step.Route {
	out := step.Route{
		Host:     r.GetHost(),
		Upstream: upstreamFromProto(r.GetUpstream()),
		TLS:      r.GetTls(),
	}
	switch r.GetMode() {
	case pb.RouteMode_ROUTE_MODE_MITM:
		out.Mode = step.RouteModeMITM
	case pb.RouteMode_ROUTE_MODE_PASSTHROUGH:
		out.Mode = step.RouteModePassthrough
	case pb.RouteMode_ROUTE_MODE_RAW:
		out.Mode = step.RouteModeRaw
	}
	if ic := r.GetIntercept(); ic != nil {
		ports := make([]int, len(ic.GetPorts()))
		for i, p := range ic.GetPorts() {
			ports[i] = int(p)
		}
		out.Intercept = &step.Intercept{Ports: ports}
	}
	return out
}

// upstreamFromProto lifts a wire Upstream. An unset one is the zero Upstream.
func upstreamFromProto(u *pb.Upstream) step.Upstream {
	if r := u.GetRelay(); r != nil {
		return step.Upstream{Relay: &step.RelayTarget{Relay: r.GetRelay(), Target: r.GetTarget()}}
	}
	return step.Upstream{Addr: u.GetAddr()}
}

// exposedPortFromProto lifts a wire ExposedPort.
func exposedPortFromProto(ep *pb.ExposedPort) step.ExposedPort {
	proto := step.TCP
	if ep.GetProtocol() == pb.Protocol_PROTOCOL_UDP {
		proto = step.UDP
	}
	return step.ExposedPort{
		Name:          ep.GetName(),
		Protocol:      proto,
		Upstream:      upstreamFromProto(ep.GetUpstream()),
		HostPort:      int(ep.GetHostPort()),
		RelayUDPAddrs: pluginwire.PortsFromProto(ep.GetRelayUdpAddrs()),
	}
}

// containersFromProto lifts a wire ContainerInfo list.
func containersFromProto(cs []*pb.ContainerInfo) []step.Container {
	if len(cs) == 0 {
		return nil
	}
	out := make([]step.Container, len(cs))
	for i, c := range cs {
		out[i] = step.Container{
			ID:           c.GetId(),
			Name:         c.GetName(),
			NetnsPath:    c.GetNetns().GetPath(),
			ExcludeCIDRs: c.GetNetns().GetExcludeCidrs(),
		}
	}
	return out
}

// containersToProto lowers a step.Container list to its wire form.
func containersToProto(cs []step.Container) []*pb.ContainerInfo {
	if len(cs) == 0 {
		return nil
	}
	out := make([]*pb.ContainerInfo, len(cs))
	for i, c := range cs {
		out[i] = &pb.ContainerInfo{Id: c.ID, Name: c.Name}
		if c.NetnsPath != "" || len(c.ExcludeCIDRs) > 0 {
			out[i].Netns = &pb.Netns{Path: c.NetnsPath, ExcludeCidrs: c.ExcludeCIDRs}
		}
	}
	return out
}

// faultFromProto lifts a wire NetworkFault.
func faultFromProto(f *pb.NetworkFault) step.Fault {
	return step.Fault{
		ID:               f.GetId(),
		NetnsPath:        f.GetNetnsPath(),
		Interface:        f.GetInterface(),
		DelayMS:          f.GetDelayMs(),
		JitterMS:         f.GetJitterMs(),
		LossPercent:      f.GetLossPercent(),
		CorruptPercent:   f.GetCorruptPercent(),
		DuplicatePercent: f.GetDuplicatePercent(),
		ReorderPercent:   f.GetReorderPercent(),
		RateKbit:         f.GetRateKbit(),
	}
}

// kindFromProto lifts a wire step kind.
func kindFromProto(k pb.StepKind) step.Kind {
	switch k {
	case pb.StepKind_STEP_KIND_RESOURCE:
		return step.KindResource
	case pb.StepKind_STEP_KIND_ACTION:
		return step.KindAction
	case pb.StepKind_STEP_KIND_PROBE:
		return step.KindProbe
	case pb.StepKind_STEP_KIND_UNSPECIFIED:
		return step.KindUnspecified
	default:
		return step.KindUnspecified
	}
}

// dispatchEvent hands a log or progress event to ev. An event of any other
// kind is ignored.
func dispatchEvent(ev step.Events, e *pb.Event) {
	switch {
	case e.GetLog() != nil:
		l := e.GetLog()
		ev.Log(l.GetStream(), l.GetText(), time.Unix(0, l.GetUnixNano()))
	case e.GetProgress() != nil:
		p := e.GetProgress()
		ev.Progress(p.GetLabel(), p.GetCurrent(), p.GetTotal())
	}
}

// outputsFromProto lifts wire outputs. A nil message or one with no values is
// a nil Map.
func outputsFromProto(o *pb.Outputs) output.Map {
	values := o.GetValues()
	if len(values) == 0 {
		return nil
	}
	out := make(output.Map, len(values))
	for k, v := range values {
		out[k] = output.Value{String: v.GetStringValue(), Sensitive: v.GetSensitive()}
	}
	return out
}

// outputsToProto lowers outputs to the wire. An empty Map is nil.
func outputsToProto(m output.Map) *pb.Outputs {
	if len(m) == 0 {
		return nil
	}
	out := &pb.Outputs{Values: make(map[string]*pb.Value, len(m))}
	for k, v := range m {
		out.Values[k] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: v.String}, Sensitive: v.Sensitive}
	}
	return out
}
