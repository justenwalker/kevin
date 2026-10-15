package launcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/step"
	"github.com/justenwalker/kevin/protos/pb"
)

func TestResultFromProto(t *testing.T) {
	t.Run("lifts every field of a full result", func(t *testing.T) {
		got := resultFromProto(&pb.Result{
			Outputs: &pb.Outputs{Values: map[string]*pb.Value{
				"url":   {Kind: &pb.Value_StringValue{StringValue: "http://x"}},
				"token": {Kind: &pb.Value_StringValue{StringValue: "s3cret"}, Sensitive: true},
			}},
			Routes: []*pb.Route{
				{
					Host:     "api.example.com",
					Upstream: &pb.Upstream{Kind: &pb.Upstream_Addr{Addr: "127.0.0.1:8080"}},
					Tls:      true,
				},
				{
					Host: "db.example.com",
					Upstream: &pb.Upstream{Kind: &pb.Upstream_Relay{
						Relay: &pb.RelayTarget{Relay: "127.0.0.1:1080", Target: "db:5432"},
					}},
					Intercept: &pb.Intercept{Ports: []int32{5432, 5433}},
					Mode:      pb.RouteMode_ROUTE_MODE_RAW,
				},
				{Host: "pass.example.com", Mode: pb.RouteMode_ROUTE_MODE_PASSTHROUGH},
			},
			EgressAllow: []string{"registry.example.com"},
			ExposedPorts: []*pb.ExposedPort{
				{
					Name:     "web",
					Upstream: &pb.Upstream{Kind: &pb.Upstream_Addr{Addr: "127.0.0.1:9000"}},
				},
				{
					Name:     "dns",
					Protocol: pb.Protocol_PROTOCOL_UDP,
					Upstream: &pb.Upstream{Kind: &pb.Upstream_Relay{
						Relay: &pb.RelayTarget{Relay: "127.0.0.1:1080", Target: "dns:53"},
					}},
					HostPort:      5353,
					RelayUdpAddrs: map[int32]string{53: "127.0.0.1:40000"},
				},
			},
			Details: []*pb.Detail{
				{Label: "password", Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "hunter2"}, Sensitive: true}, Copyable: true},
				{Label: "docs", Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "here"}}, Href: "http://docs"},
			},
			Containers: []*pb.ContainerInfo{
				{Id: "abc", Name: "web", Netns: &pb.Netns{Path: "/proc/1/ns/net", ExcludeCidrs: []string{"10.0.0.0/8"}}},
				{Id: "def", Name: "sidecar"},
			},
			Faults: []*pb.NetworkFault{{
				Id: "f1", NetnsPath: "/proc/1/ns/net", Interface: "eth0",
				DelayMs: 100, JitterMs: 10, LossPercent: 1.5, CorruptPercent: 2.5,
				DuplicatePercent: 3.5, ReorderPercent: 4.5, RateKbit: 512,
			}},
		})

		want := step.Result{
			Outputs: output.Map{
				"url":   {String: "http://x"},
				"token": {String: "s3cret", Sensitive: true},
			},
			Routes: []step.Route{
				{Host: "api.example.com", Upstream: step.Upstream{Addr: "127.0.0.1:8080"}, TLS: true},
				{
					Host:      "db.example.com",
					Upstream:  step.Upstream{Relay: &step.RelayTarget{Relay: "127.0.0.1:1080", Target: "db:5432"}},
					Intercept: &step.Intercept{Ports: []int{5432, 5433}},
					Mode:      step.RouteModeRaw,
				},
				{Host: "pass.example.com", Mode: step.RouteModePassthrough},
			},
			EgressAllow: []string{"registry.example.com"},
			ExposedPorts: []step.ExposedPort{
				{Name: "web", Protocol: step.TCP, Upstream: step.Upstream{Addr: "127.0.0.1:9000"}},
				{
					Name:          "dns",
					Protocol:      step.UDP,
					Upstream:      step.Upstream{Relay: &step.RelayTarget{Relay: "127.0.0.1:1080", Target: "dns:53"}},
					HostPort:      5353,
					RelayUDPAddrs: map[int]string{53: "127.0.0.1:40000"},
				},
			},
			Details: []step.Detail{
				{Label: "password", Value: output.Value{String: "hunter2", Sensitive: true}, Copyable: true},
				{Label: "docs", Value: output.Value{String: "here"}, Href: "http://docs"},
			},
			Containers: []step.Container{
				{ID: "abc", Name: "web", NetnsPath: "/proc/1/ns/net", ExcludeCIDRs: []string{"10.0.0.0/8"}},
				{ID: "def", Name: "sidecar"},
			},
			Faults: []step.Fault{{
				ID: "f1", NetnsPath: "/proc/1/ns/net", Interface: "eth0",
				DelayMS: 100, JitterMS: 10, LossPercent: 1.5, CorruptPercent: 2.5,
				DuplicatePercent: 3.5, ReorderPercent: 4.5, RateKbit: 512,
			}},
		}
		assert.Equal(t, want, got)
	})

	t.Run("a nil or empty message is an empty result", func(t *testing.T) {
		assert.Equal(t, step.Result{}, resultFromProto(nil))
		assert.Equal(t, step.Result{}, resultFromProto(&pb.Result{}))
	})
}

func TestUpstreamDial(t *testing.T) {
	addr := resultFromProto(&pb.Result{Routes: []*pb.Route{{
		Upstream: &pb.Upstream{Kind: &pb.Upstream_Addr{Addr: "127.0.0.1:80"}},
	}}}).Routes[0].Upstream
	relay := resultFromProto(&pb.Result{Routes: []*pb.Route{{
		Upstream: &pb.Upstream{Kind: &pb.Upstream_Relay{Relay: &pb.RelayTarget{Relay: "r:1", Target: "t:2"}}},
	}}}).Routes[0].Upstream

	assert.Equal(t, "127.0.0.1:80", addr.Dial())
	assert.Equal(t, "socks5://r:1/t:2", relay.Dial())
}

func TestContainers(t *testing.T) {
	t.Run("round trips through the wire form", func(t *testing.T) {
		in := []step.Container{
			{ID: "abc", Name: "web", NetnsPath: "/proc/1/ns/net", ExcludeCIDRs: []string{"10.0.0.0/8"}},
			{ID: "def", Name: "sidecar"},
		}
		assert.Equal(t, in, containersFromProto(containersToProto(in)))
	})

	t.Run("a nil list stays nil", func(t *testing.T) {
		assert.Nil(t, containersToProto(nil))
		assert.Nil(t, containersFromProto(nil))
	})
}

func TestExportResultFromProto(t *testing.T) {
	got := exportResultFromProto(&pb.ExportResponse{
		Out:        &pb.Outputs{Values: map[string]*pb.Value{"k": {Kind: &pb.Value_StringValue{StringValue: "v"}, Sensitive: true}}},
		Containers: []*pb.ContainerInfo{{Id: "abc", Name: "web"}},
	})
	assert.Equal(t, output.Map{"k": {String: "v", Sensitive: true}}, got.Out)
	assert.Equal(t, []step.Container{{ID: "abc", Name: "web"}}, got.Containers)
	assert.Equal(t, step.ExportResult{}, exportResultFromProto(nil))
}

func TestToolResultFromProto(t *testing.T) {
	got := toolResultFromProto(&pb.ToolCallResponse{Content: []byte(`{}`), IsError: true, ErrorMessage: "bad"})
	assert.Equal(t, step.ToolResult{Content: []byte(`{}`), IsError: true, ErrorMessage: "bad"}, got)
	assert.Equal(t, step.ToolResult{}, toolResultFromProto(nil))
}

func TestEnvToProto(t *testing.T) {
	t.Run("lowers every field", func(t *testing.T) {
		got := envToProto(step.Env{
			Project:             "p",
			Workspace:           "/ws",
			Network:             "net",
			CAPath:              "/ca.pem",
			HTTPProxyAddr:       "127.0.0.1:3128",
			ConsoleAddr:         "127.0.0.1:8080",
			ProxyEnv:            map[string]string{"HTTP_PROXY": "x"},
			Domain:              "kevin.test",
			Relay:               "127.0.0.1:9000",
			RelaySOCKS5Addr:     "127.0.0.1:1080",
			RelaySOCKS5UDPAddrs: map[int]string{53: "127.0.0.1:40000"},
			ProjectDir:          "/proj",
			Scope:               config.ScopeSetup,
			Engine:              step.EnginePodman,
		})
		want := &pb.Environment{
			Project:             "p",
			Workspace:           "/ws",
			Network:             "net",
			CaPath:              "/ca.pem",
			HttpProxyAddr:       "127.0.0.1:3128",
			ConsoleAddr:         "127.0.0.1:8080",
			ProxyEnv:            map[string]string{"HTTP_PROXY": "x"},
			Domain:              "kevin.test",
			Relay:               "127.0.0.1:9000",
			RelaySocks5Addr:     "127.0.0.1:1080",
			RelaySocks5UdpAddrs: map[int32]string{53: "127.0.0.1:40000"},
			ProjectDir:          "/proj",
			Scope:               pb.Scope_SCOPE_SETUP,
			Engine:              pb.Engine_ENGINE_PODMAN,
		}
		require.Equal(t, want.String(), got.String())
	})

	t.Run("maps scope and engine", func(t *testing.T) {
		tests := []struct {
			name       string
			scope      config.Scope
			engine     step.Engine
			wantScope  pb.Scope
			wantEngine pb.Engine
		}{
			{name: "env and docker", scope: config.ScopeEnv, engine: step.EngineDocker, wantScope: pb.Scope_SCOPE_ENV, wantEngine: pb.Engine_ENGINE_DOCKER},
			{name: "setup and podman", scope: config.ScopeSetup, engine: step.EnginePodman, wantScope: pb.Scope_SCOPE_SETUP, wantEngine: pb.Engine_ENGINE_PODMAN},
			{name: "the zero engine is docker", scope: config.ScopeEnv, wantScope: pb.Scope_SCOPE_ENV, wantEngine: pb.Engine_ENGINE_DOCKER},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := envToProto(step.Env{Scope: tt.scope, Engine: tt.engine})
				assert.Equal(t, tt.wantScope, got.GetScope())
				assert.Equal(t, tt.wantEngine, got.GetEngine())
			})
		}
	})
}

func TestUpRequestToProto(t *testing.T) {
	got := upRequestToProto(step.UpRequest{
		Step:   "api",
		Type:   "container",
		Env:    step.Env{Scope: config.ScopeEnv},
		Config: []byte(`{}`),
		Deps:   map[string]output.Map{"db": {"host": {String: "h", Sensitive: true}}},
		Containers: []step.StepContainers{{
			Scope:      config.ScopeSetup,
			Step:       "cluster",
			Containers: []step.Container{{ID: "abc", Name: "node", NetnsPath: "/ns"}},
		}},
	})
	assert.Equal(t, "api", got.GetStep())
	assert.Equal(t, "container", got.GetType())
	assert.Equal(t, []byte(`{}`), got.GetConfig())
	assert.Equal(t, pb.Scope_SCOPE_ENV, got.GetEnv().GetScope())
	assert.Equal(t, "h", got.GetDeps()["db"].GetValues()["host"].GetStringValue())
	assert.True(t, got.GetDeps()["db"].GetValues()["host"].GetSensitive())
	require.Len(t, got.GetContainers(), 1)
	assert.Equal(t, pb.Scope_SCOPE_SETUP, got.GetContainers()[0].GetScope())
	assert.Equal(t, "cluster", got.GetContainers()[0].GetStep())
	assert.Equal(t, "/ns", got.GetContainers()[0].GetContainers()[0].GetNetns().GetPath())
}

func TestKindFromProto(t *testing.T) {
	assert.Equal(t, step.KindUnspecified, kindFromProto(pb.StepKind_STEP_KIND_UNSPECIFIED))
	assert.Equal(t, step.KindResource, kindFromProto(pb.StepKind_STEP_KIND_RESOURCE))
	assert.Equal(t, step.KindAction, kindFromProto(pb.StepKind_STEP_KIND_ACTION))
	assert.Equal(t, step.KindProbe, kindFromProto(pb.StepKind_STEP_KIND_PROBE))
}

func TestOutputsFromProto(t *testing.T) {
	t.Run("is nil without values", func(t *testing.T) {
		assert.Nil(t, outputsFromProto(nil))
		assert.Nil(t, outputsFromProto(&pb.Outputs{}))
	})

	t.Run("keeps the value and its sensitivity", func(t *testing.T) {
		got := outputsFromProto(&pb.Outputs{Values: map[string]*pb.Value{
			"url":   {Kind: &pb.Value_StringValue{StringValue: "http://x"}},
			"token": {Kind: &pb.Value_StringValue{StringValue: "s3"}, Sensitive: true},
		}})

		assert.Equal(t, output.Map{"url": {String: "http://x"}, "token": {String: "s3", Sensitive: true}}, got)
	})
}

func TestOutputsToProto(t *testing.T) {
	t.Run("is nil when empty", func(t *testing.T) {
		assert.Nil(t, outputsToProto(nil))
		assert.Nil(t, outputsToProto(output.Map{}))
	})

	t.Run("keeps the value and its sensitivity", func(t *testing.T) {
		got := outputsToProto(output.Map{"k": {String: "v"}, "secret": {String: "s", Sensitive: true}})

		assert.Equal(t, &pb.Outputs{Values: map[string]*pb.Value{
			"k":      {Kind: &pb.Value_StringValue{StringValue: "v"}},
			"secret": {Kind: &pb.Value_StringValue{StringValue: "s"}, Sensitive: true},
		}}, got)
	})
}
