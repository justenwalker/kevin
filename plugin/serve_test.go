package plugin

import (
	"context"
	"testing"

	goplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestGRPCPlugin(t *testing.T) {
	t.Run("GRPCServer registers the plugin service", func(t *testing.T) {
		p := &GRPCPlugin{Plugin: Plugin{Name: "demo"}}
		s := grpc.NewServer()

		require.NoError(t, p.GRPCServer(nil, s))

		_, ok := s.GetServiceInfo()[pb.Plugin_ServiceDesc.ServiceName]
		assert.True(t, ok, "GRPCServer must register the plugin service")
	})

	t.Run("GRPCClient returns a plugin client bound to the connection", func(t *testing.T) {
		p := &GRPCPlugin{}
		conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		defer conn.Close() //nolint:errcheck // test cleanup

		client, err := p.GRPCClient(t.Context(), (*goplugin.GRPCBroker)(nil), conn)
		require.NoError(t, err)
		assert.Implements(t, (*pb.PluginClient)(nil), client)
	})
}

func TestServerInfo(t *testing.T) {
	t.Run("reports the provider and every step type", func(t *testing.T) {
		widget := NewMockStep(t)
		widget.EXPECT().Schema().Return([]byte("#Config: {widget: true}"))
		widget.EXPECT().Kind().Return(StepKindUnspecified)

		gadget := NewMockStep(t)
		gadget.EXPECT().Schema().Return([]byte("#Config: {gadget: true}"))
		gadget.EXPECT().Kind().Return(StepKindUnspecified)

		provider := Plugin{
			Name:         "demo",
			Version:      "1.2.3",
			ConfigSchema: []byte("#Config: {}"),
			Steps:        map[string]Step{"widget": widget, "gadget": gadget},
			Icon:         []byte("fake-png-bytes"),
		}
		srv := &server{provider: provider}

		resp, err := srv.Info(t.Context(), &pb.InfoRequest{})
		require.NoError(t, err)

		assert.Equal(t, "demo", resp.GetName())
		assert.Equal(t, "1.2.3", resp.GetVersion())
		assert.Equal(t, []byte("#Config: {}"), resp.GetConfigSchema())
		assert.Equal(t, []byte("fake-png-bytes"), resp.GetIcon(), "Info must pass the provider's icon through unchanged")

		require.Len(t, resp.GetSteps(), 2, "Info must report every step type")
		assert.Equal(t, "gadget", resp.GetSteps()[0].GetName(), "steps must be sorted by name")
		assert.Equal(t, []byte("#Config: {gadget: true}"), resp.GetSteps()[0].GetCueSchema())
		assert.Equal(t, "widget", resp.GetSteps()[1].GetName())
	})

	t.Run("reports per-step capabilities", func(t *testing.T) {
		tests := []struct {
			name       string
			step       func(t *testing.T) Step
			wantExport bool
			wantDown   bool
			wantKind   pb.StepKind
		}{
			{
				name: "no extra capabilities",
				step: func(t *testing.T) Step {
					t.Helper()
					m := NewMockStep(t)
					m.EXPECT().Schema().Return(nil)
					m.EXPECT().Kind().Return(StepKindResource)
					return m
				},
				wantKind: pb.StepKind_STEP_KIND_RESOURCE,
			},
			{
				name: "exporter reports export: true",
				step: func(t *testing.T) Step {
					t.Helper()
					m := NewMockStep(t)
					m.EXPECT().Schema().Return(nil)
					m.EXPECT().Kind().Return(StepKindUnspecified)
					return &stepWithExporter{MockStep: m, MockExporter: NewMockExporter(t)}
				},
				wantExport: true,
			},
			{
				name: "downer reports down: true",
				step: func(t *testing.T) Step {
					t.Helper()
					m := NewMockStep(t)
					m.EXPECT().Schema().Return(nil)
					m.EXPECT().Kind().Return(StepKindUnspecified)
					return &stepWithDowner{MockStep: m, MockDowner: NewMockDowner(t)}
				},
				wantDown: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				srv := &server{provider: Plugin{Steps: map[string]Step{"widget": tt.step(t)}}}

				resp, err := srv.Info(t.Context(), &pb.InfoRequest{})
				require.NoError(t, err)

				require.Len(t, resp.GetSteps(), 1)
				st := resp.GetSteps()[0]
				assert.Equal(t, tt.wantExport, st.GetExport())
				assert.Equal(t, tt.wantDown, st.GetDown())
				assert.Equal(t, tt.wantKind, st.GetKind())
			})
		}
	})

	t.Run("reports a tool provider's tools", func(t *testing.T) {
		m := NewMockStep(t)
		m.EXPECT().Schema().Return(nil)
		m.EXPECT().Kind().Return(StepKindUnspecified)
		toolProvider := NewMockToolProvider(t)
		toolProvider.EXPECT().Tools().Return([]ToolDef{
			{Name: "query", Description: "runs a query", InputSchema: []byte(`{"type":"object"}`)},
		})
		impl := &stepWithToolProvider{MockStep: m, MockToolProvider: toolProvider}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}

		resp, err := srv.Info(t.Context(), &pb.InfoRequest{})
		require.NoError(t, err)

		require.Len(t, resp.GetSteps(), 1)
		tools := resp.GetSteps()[0].GetTools()
		require.Len(t, tools, 1)
		assert.Equal(t, "query", tools[0].GetName())
		assert.Equal(t, "runs a query", tools[0].GetDescription())
		assert.JSONEq(t, `{"type":"object"}`, string(tools[0].GetInputSchema()))
	})

	t.Run("a step with no tool provider reports no tools", func(t *testing.T) {
		m := NewMockStep(t)
		m.EXPECT().Schema().Return(nil)
		m.EXPECT().Kind().Return(StepKindUnspecified)
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": m}}}

		resp, err := srv.Info(t.Context(), &pb.InfoRequest{})
		require.NoError(t, err)

		require.Len(t, resp.GetSteps(), 1)
		assert.Empty(t, resp.GetSteps()[0].GetTools())
	})
}

func TestServerCallTool(t *testing.T) {
	t.Run("routes to the step and translates the result", func(t *testing.T) {
		var gotReq *ToolCallRequest
		toolProvider := NewMockToolProvider(t)
		toolProvider.EXPECT().CallTool(mock.Anything, mock.Anything).
			Run(func(_ context.Context, req *ToolCallRequest) { gotReq = req }).
			Return(&ToolCallResult{Content: map[string]any{"rows": float64(3)}}, nil)
		impl := &stepWithToolProvider{MockStep: NewMockStep(t), MockToolProvider: toolProvider}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}

		resp, err := srv.CallTool(t.Context(), &pb.ToolCallRequest{
			Step: "db", Type: "widget", Tool: "query", Config: []byte(`{"a":1}`), Arguments: []byte(`{"sql":"select 1"}`),
		})
		require.NoError(t, err)
		assert.False(t, resp.GetIsError())
		assert.JSONEq(t, `{"rows":3}`, string(resp.GetContent()))

		require.NotNil(t, gotReq)
		assert.Equal(t, "db", gotReq.Step)
		assert.Equal(t, "widget", gotReq.Type)
		assert.Equal(t, "query", gotReq.Tool)
		assert.JSONEq(t, `{"sql":"select 1"}`, string(gotReq.Arguments))
	})

	t.Run("carries a tool-reported error through", func(t *testing.T) {
		toolProvider := NewMockToolProvider(t)
		toolProvider.EXPECT().CallTool(mock.Anything, mock.Anything).
			Return(&ToolCallResult{IsError: true, ErrorMessage: "no such table"}, nil)
		impl := &stepWithToolProvider{MockStep: NewMockStep(t), MockToolProvider: toolProvider}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}

		resp, err := srv.CallTool(t.Context(), &pb.ToolCallRequest{Step: "db", Type: "widget", Tool: "query"})
		require.NoError(t, err, "a tool-level failure is not an RPC error")
		assert.True(t, resp.GetIsError())
		assert.Equal(t, "no such table", resp.GetErrorMessage())
	})

	t.Run("returns the step error", func(t *testing.T) {
		toolProvider := NewMockToolProvider(t)
		toolProvider.EXPECT().CallTool(mock.Anything, mock.Anything).Return(nil, assert.AnError)
		impl := &stepWithToolProvider{MockStep: NewMockStep(t), MockToolProvider: toolProvider}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}

		_, err := srv.CallTool(t.Context(), &pb.ToolCallRequest{Step: "db", Type: "widget", Tool: "query"})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("reports unimplemented for a step type with no tools", func(t *testing.T) {
		srv := &server{provider: Plugin{Name: "demo", Steps: map[string]Step{"widget": NewMockStep(t)}}}

		_, err := srv.CallTool(t.Context(), &pb.ToolCallRequest{Step: "db", Type: "widget", Tool: "query"})
		require.Error(t, err)
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})

	t.Run("reports an unknown step type", func(t *testing.T) {
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": NewMockStep(t)}}}

		_, err := srv.CallTool(t.Context(), &pb.ToolCallRequest{Step: "db", Type: "sprocket", Tool: "query"})
		require.ErrorIs(t, err, ErrUnknownStepType)
	})
}

func TestServerExport(t *testing.T) {
	t.Run("routes to the step and translates the result", func(t *testing.T) {
		var gotExport *ExportRequest
		exporter := NewMockExporter(t)
		exporter.EXPECT().Export(mock.Anything, mock.Anything).
			Run(func(_ context.Context, req *ExportRequest) { gotExport = req }).
			Return(&ExportResult{Out: StringMap(map[string]string{"kubeconfig": "/tmp/kubeconfig"})}, nil)
		impl := &stepWithExporter{MockStep: NewMockStep(t), MockExporter: exporter}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}

		resp, err := srv.Export(t.Context(), &pb.ExportRequest{Step: "cluster", Type: "widget", Config: []byte(`{"a":1}`)})
		require.NoError(t, err)
		require.NotNil(t, resp.GetOut())
		assert.Equal(t, "/tmp/kubeconfig", resp.GetOut().GetValues()["kubeconfig"].GetStringValue())

		require.NotNil(t, gotExport)
		assert.Equal(t, "cluster", gotExport.Step)
		assert.Equal(t, "widget", gotExport.Type)
		assert.JSONEq(t, `{"a":1}`, string(gotExport.Config))
	})

	t.Run("treats a nil result the same as an empty one", func(t *testing.T) {
		exporter := NewMockExporter(t)
		exporter.EXPECT().Export(mock.Anything, mock.Anything).Return(nil, nil)
		impl := &stepWithExporter{MockStep: NewMockStep(t), MockExporter: exporter}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}

		resp, err := srv.Export(t.Context(), &pb.ExportRequest{Step: "a", Type: "widget"})
		require.NoError(t, err)
		assert.Empty(t, resp.GetOut().GetValues())
	})

	t.Run("returns the step error", func(t *testing.T) {
		exporter := NewMockExporter(t)
		exporter.EXPECT().Export(mock.Anything, mock.Anything).Return(nil, assert.AnError)
		impl := &stepWithExporter{MockStep: NewMockStep(t), MockExporter: exporter}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}

		_, err := srv.Export(t.Context(), &pb.ExportRequest{Step: "a", Type: "widget"})
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("reports unimplemented for a non-exporter", func(t *testing.T) {
		srv := &server{provider: Plugin{Name: "demo", Steps: map[string]Step{"widget": NewMockStep(t)}}}

		_, err := srv.Export(t.Context(), &pb.ExportRequest{Step: "a", Type: "widget"})
		require.Error(t, err)
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})

	t.Run("reports an unknown step type", func(t *testing.T) {
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": NewMockStep(t)}}}

		_, err := srv.Export(t.Context(), &pb.ExportRequest{Step: "a", Type: "sprocket"})
		require.ErrorIs(t, err, ErrUnknownStepType)
	})
}

func TestServerUp(t *testing.T) {
	t.Run("routes to the named step type", func(t *testing.T) {
		widget := NewMockStep(t)
		gadget := NewMockStep(t)
		var gotUp *UpRequest
		gadget.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, req *UpRequest, _ Emitter) { gotUp = req }).
			Return(&Result{Outputs: map[string]Value{"k": String("gadget")}}, nil)

		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": widget, "gadget": gadget}}}
		stream := &fakeStream{}
		require.NoError(t, srv.Up(&pb.UpRequest{Step: "a", Type: "gadget"}, stream))

		widget.AssertNotCalled(t, "Up", mock.Anything, mock.Anything, mock.Anything)
		require.NotNil(t, gotUp, "Up must reach the step type that the request names")
		assert.Equal(t, "a", gotUp.Step)
		assert.Equal(t, "gadget", gotUp.Type)
	})

	t.Run("translates the request and the result", func(t *testing.T) {
		var gotUp *UpRequest
		impl := NewMockStep(t)
		impl.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, req *UpRequest, out Emitter) {
				gotUp = req
				out.Log("stdout", "first")
				out.Log("stdout", "second")
				out.Progress("half way", 1, 2)
			}).
			Return(&Result{
				Outputs: map[string]Value{
					"endpoint": String("10.0.0.2:5000"),
					"password": Sensitive{String("hunter2")},
				},
				Routes:       []Route{{Host: "api.test", Upstream: "api:8080", TLS: true}},
				ExposedPorts: []ExposedPort{{Name: "postgres", Protocol: "tcp", Upstream: "127.0.0.1:54321", HostPort: 54321}},
				EgressAllow:  []string{"proxy.golang.org"},
				Details:      []Detail{{Label: "admin password", Value: Sensitive{String("hunter2")}, Copyable: true, Href: "https://api.test/admin"}},
			}, nil)
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}
		stream := &fakeStream{}

		req := &pb.UpRequest{
			Step: "api",
			Type: "widget",
			Env: &pb.Environment{
				Project:       "demo",
				Workspace:     "/tmp/demo/.kevin",
				Network:       "kevin-demo",
				CaPath:        "/home/user/.kevin/root.crt",
				HttpProxyAddr: "127.0.0.1:8080",
				ConsoleAddr:   "127.0.0.1:8081",
				ProxyEnv:      map[string]string{"HTTP_PROXY": "http://127.0.0.1:8080"},
				Domain:        "kevin.home",
				Relay:         "172.20.0.5",
			},
			Config: []byte(`{"image":"nginx"}`),
			Deps: map[string]*pb.Outputs{"db": {
				Values: map[string]*pb.Value{
					"dsn":           {Kind: &pb.Value_StringValue{StringValue: "postgres://"}},
					"root_password": {Kind: &pb.Value_StringValue{StringValue: "s3cret"}, Sensitive: true},
				},
			}},
		}
		require.NoError(t, srv.Up(req, stream))

		// The request reached the Step with every field intact.
		require.NotNil(t, gotUp)
		assert.Equal(t, "api", gotUp.Step)
		assert.Equal(t, "widget", gotUp.Type)
		assert.JSONEq(t, `{"image":"nginx"}`, string(gotUp.Config))
		assert.Equal(t, map[string]map[string]Value{"db": {
			"dsn":           String("postgres://"),
			"root_password": Sensitive{String("s3cret")},
		}}, gotUp.Deps)
		assert.Equal(t, Env{
			Project:       "demo",
			Workspace:     "/tmp/demo/.kevin",
			Network:       "kevin-demo",
			CAPath:        "/home/user/.kevin/root.crt",
			HTTPProxyAddr: "127.0.0.1:8080",
			ConsoleAddr:   "127.0.0.1:8081",
			ProxyEnv:      map[string]string{"HTTP_PROXY": "http://127.0.0.1:8080"},
			Domain:        "kevin.home",
			Relay:         "172.20.0.5",
		}, gotUp.Env)

		// The stream carried the logs, then the progress, then one final result.
		require.Len(t, stream.events, 4)
		assert.Equal(t, "first", stream.events[0].GetLog().GetText())
		assert.Equal(t, "stdout", stream.events[0].GetLog().GetStream())
		assert.Positive(t, stream.events[0].GetLog().GetUnixNano())
		assert.Equal(t, "second", stream.events[1].GetLog().GetText())

		progress := stream.events[2].GetProgress()
		assert.Equal(t, "half way", progress.GetLabel())
		assert.Equal(t, int64(1), progress.GetCurrent())
		assert.Equal(t, int64(2), progress.GetTotal())

		result := stream.events[3].GetResult()
		require.NotNil(t, result, "the last event must be the result")
		require.Len(t, result.GetOutputs().GetValues(), 2)
		assert.Equal(t, "10.0.0.2:5000", result.GetOutputs().GetValues()["endpoint"].GetStringValue())
		assert.False(t, result.GetOutputs().GetValues()["endpoint"].GetSensitive())
		assert.Equal(t, "hunter2", result.GetOutputs().GetValues()["password"].GetStringValue())
		assert.True(t, result.GetOutputs().GetValues()["password"].GetSensitive())
		assert.Equal(t, []string{"proxy.golang.org"}, result.GetEgressAllow())
		require.Len(t, result.GetRoutes(), 1)
		assert.Equal(t, "api.test", result.GetRoutes()[0].GetHost())
		assert.Equal(t, "api:8080", result.GetRoutes()[0].GetUpstream())
		assert.True(t, result.GetRoutes()[0].GetTls())

		require.Len(t, result.GetExposedPorts(), 1)
		assert.Equal(t, "postgres", result.GetExposedPorts()[0].GetName())
		assert.Equal(t, "tcp", result.GetExposedPorts()[0].GetProtocol())
		assert.Equal(t, "127.0.0.1:54321", result.GetExposedPorts()[0].GetUpstream())
		assert.Equal(t, int32(54321), result.GetExposedPorts()[0].GetHostPort())

		require.Len(t, result.GetDetails(), 1)
		assert.Equal(t, "admin password", result.GetDetails()[0].GetLabel())
		assert.Equal(t, "hunter2", result.GetDetails()[0].GetValue().GetStringValue())
		assert.True(t, result.GetDetails()[0].GetCopyable())
		assert.Equal(t, "https://api.test/admin", result.GetDetails()[0].GetHref())
		assert.True(t, result.GetDetails()[0].GetValue().GetSensitive())
	})

	t.Run("sends a result when the step returns none", func(t *testing.T) {
		impl := NewMockStep(t)
		impl.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}
		stream := &fakeStream{}

		require.NoError(t, srv.Up(&pb.UpRequest{Step: "a", Type: "widget"}, stream))

		last := stream.events[len(stream.events)-1].GetResult()
		require.NotNil(t, last, "a nil Result must still terminate the stream")
		assert.Empty(t, last.GetOutputs().GetValues())
	})

	t.Run("returns the step error", func(t *testing.T) {
		impl := NewMockStep(t)
		impl.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(nil, assert.AnError)
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}
		stream := &fakeStream{}

		err := srv.Up(&pb.UpRequest{Step: "a", Type: "widget"}, stream)
		require.ErrorIs(t, err, assert.AnError)

		for _, ev := range stream.events {
			assert.Nil(t, ev.GetResult(), "a failed Up must send no result")
		}
	})

	t.Run("reports an unknown step type and names what is offered", func(t *testing.T) {
		srv := &server{provider: Plugin{
			Name:  "demo",
			Steps: map[string]Step{"widget": NewMockStep(t), "gadget": NewMockStep(t)},
		}}

		err := srv.Up(&pb.UpRequest{Step: "a", Type: "sprocket"}, &fakeStream{})
		require.ErrorIs(t, err, ErrUnknownStepType)
		assert.Contains(t, err.Error(), "sprocket", "the error must name the type that was requested")
		assert.Contains(t, err.Error(), "widget", "the error must name what the provider offers")
		assert.Contains(t, err.Error(), "gadget", "the error must name what the provider offers")
	})

	t.Run("wraps a failure to send the result", func(t *testing.T) {
		impl := NewMockStep(t)
		impl.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).Return(&Result{}, nil)
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}
		stream := &fakeStream{err: assert.AnError}

		err := srv.Up(&pb.UpRequest{Step: "a", Type: "widget"}, stream)
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("attaches a human-facing message as a status detail", func(t *testing.T) {
		impl := NewMockStep(t)
		impl.EXPECT().Up(mock.Anything, mock.Anything, mock.Anything).
			Return(nil, Wrap(assert.AnError, "the widget %s is out of stock", "acme"))
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}
		stream := &fakeStream{}

		err := srv.Up(&pb.UpRequest{Step: "a", Type: "widget"}, stream)
		require.Error(t, err)
		assert.Equal(t, codes.Unknown, status.Code(err))

		st, ok := status.FromError(err)
		require.True(t, ok)
		require.Len(t, st.Details(), 1)
		msg, ok := st.Details()[0].(*pb.UserMessage)
		require.True(t, ok)
		assert.Equal(t, "the widget %s is out of stock", msg.GetKey())
		assert.Equal(t, []string{"acme"}, msg.GetArgs())
	})
}

func TestServerDown(t *testing.T) {
	t.Run("calls down and streams the log", func(t *testing.T) {
		var gotDown *DownRequest
		downer := NewMockDowner(t)
		downer.EXPECT().Down(mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, req *DownRequest, out Emitter) {
				gotDown = req
				out.Log("stderr", "removing")
			}).
			Return(nil)
		impl := &stepWithDowner{MockStep: NewMockStep(t), MockDowner: downer}
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": impl}}}
		stream := &fakeStream{}

		req := &pb.DownRequest{
			Step:    "api",
			Type:    "widget",
			Env:     &pb.Environment{Project: "demo"},
			Config:  []byte(`{}`),
			Outputs: &pb.Outputs{Values: map[string]*pb.Value{"id": {Kind: &pb.Value_StringValue{StringValue: "abc"}}}},
		}
		require.NoError(t, srv.Down(req, stream))

		require.NotNil(t, gotDown)
		assert.Equal(t, "api", gotDown.Step)
		assert.Equal(t, "widget", gotDown.Type)
		assert.Equal(t, "demo", gotDown.Env.Project)
		assert.Equal(t, map[string]Value{"id": String("abc")}, gotDown.Outputs)

		require.Len(t, stream.events, 1)
		assert.Equal(t, "stderr", stream.events[0].GetLog().GetStream())
	})

	t.Run("reports unimplemented for a non-downer", func(t *testing.T) {
		srv := &server{provider: Plugin{Name: "demo", Steps: map[string]Step{"widget": NewMockStep(t)}}}

		err := srv.Down(&pb.DownRequest{Step: "a", Type: "widget"}, &fakeStream{})
		require.Error(t, err)
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})

	t.Run("reports an unknown step type", func(t *testing.T) {
		srv := &server{provider: Plugin{Steps: map[string]Step{"widget": NewMockStep(t)}}}

		err := srv.Down(&pb.DownRequest{Step: "a", Type: "sprocket"}, &fakeStream{})
		require.ErrorIs(t, err, ErrUnknownStepType)
		assert.Contains(t, err.Error(), "sprocket")
	})
}

func TestServerConfigure(t *testing.T) {
	tests := []struct {
		name         string
		hasConfigure bool
		configureErr error
		config       []byte
		wantErr      error
	}{
		{name: "calls the provider with the config", hasConfigure: true, config: []byte(`{"k":"v"}`)},
		{name: "nil Configure and empty config succeeds", hasConfigure: false, config: nil},
		{
			name: "nil Configure and a config fails", hasConfigure: false, config: []byte(`{"k":"v"}`),
			wantErr: ErrConfigureNotSupported,
		},
		{
			name: "the provider's Configure returns an error", hasConfigure: true, config: []byte(`{"k":"v"}`),
			configureErr: assert.AnError, wantErr: assert.AnError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotConfig []byte
			var gotEnv Env
			var provider Plugin
			if tt.hasConfigure {
				provider.Configure = func(_ context.Context, config []byte, env Env) error {
					gotConfig = config
					gotEnv = env
					return tt.configureErr
				}
			}
			srv := &server{provider: provider}

			resp, err := srv.Configure(t.Context(), &pb.ConfigureRequest{
				Config: tt.config,
				Env:    &pb.Environment{Project: "demo"},
			})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "Configure must not discard the error")
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, resp)
			if tt.hasConfigure {
				assert.JSONEq(t, string(tt.config), string(gotConfig))
				assert.Equal(t, "demo", gotEnv.Project)
			}
		})
	}
}

func TestDepsFromProto(t *testing.T) {
	tests := []struct {
		name string
		deps map[string]*pb.Outputs
	}{
		{name: "nil map", deps: nil},
		{name: "empty map", deps: map[string]*pb.Outputs{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Nil(t, depsFromProto(tt.deps))
		})
	}
}

func TestEmitterIgnoresASendFailure(t *testing.T) {
	// A closed stream must not panic and must not block a Step.
	e := &emitter{stream: &fakeStream{err: assert.AnError}}
	e.Log("stdout", "dropped")
	e.Progress("dropped", 0, 0)
}

// fakeStream collects the events that the server sends. The gRPC transport is
// absent, so only Send and Context need a real implementation.
type fakeStream struct {
	grpc.ServerStream

	ctx    context.Context //nolint:containedctx // the gRPC stream interface holds one
	events []*pb.Event
	err    error
}

func (s *fakeStream) Send(ev *pb.Event) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, ev)
	return nil
}

func (s *fakeStream) Context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// stepWithExporter combines a MockStep and a MockExporter into one value
// that satisfies both Step and Exporter, the way a real plugin step would.
type stepWithExporter struct {
	*MockStep
	*MockExporter
}

// stepWithDowner combines a MockStep and a MockDowner into one value that
// satisfies both Step and Downer, the way a real plugin step would.
type stepWithDowner struct {
	*MockStep
	*MockDowner
}

// stepWithToolProvider combines a MockStep and a MockToolProvider into
// one value that satisfies both Step and ToolProvider, the way a real
// plugin step would.
type stepWithToolProvider struct {
	*MockStep
	*MockToolProvider
}
