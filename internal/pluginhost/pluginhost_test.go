package pluginhost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/protos/pb"
)

// buildEchoPlugin builds the echo plugin binary. It reports Name: "echo".
func buildEchoPlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kevin-plugin-echo")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin,
		"github.com/justenwalker/kevin/cmd/kevin-plugin-echo")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build echo plugin: %s", out)
	return bin
}

func TestLaunch(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		spec       Spec
		wantErr    string
	}{
		{
			name:       "a missing binary",
			pluginName: "ghost",
			spec:       Spec{Cmd: filepath.Join(t.TempDir(), "no-such-plugin"), Dir: t.TempDir()},
			wantErr:    "ghost",
		},
		{
			// A program that exits at once cannot complete the handshake.
			name:       "a binary that does not speak the protocol",
			pluginName: "true",
			spec:       Spec{Cmd: "/usr/bin/true", Dir: t.TempDir()},
			wantErr:    "true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Launch(t.Context(), tt.pluginName, tt.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	t.Run("resolves a relative binary against Dir", func(t *testing.T) {
		// A bare filename (no separator) is left for exec.Command to find on
		// PATH instead, so the relative path needs a directory component to
		// exercise the join against Dir.
		dir := t.TempDir()
		bin := filepath.Join(dir, "sub", "kevin-plugin-echo")
		require.NoError(t, os.MkdirAll(filepath.Dir(bin), 0o750))
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin,
			"github.com/justenwalker/kevin/cmd/kevin-plugin-echo")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "build echo plugin: %s", out)

		client, err := Launch(t.Context(), "echo", Spec{Cmd: filepath.Join("sub", "kevin-plugin-echo"), Dir: dir})
		require.NoError(t, err)
		t.Cleanup(client.Close)

		_, err = client.Info(t.Context())
		require.NoError(t, err)
	})
}

func TestInfo(t *testing.T) {
	bin := buildEchoPlugin(t)

	t.Run("checks the name", func(t *testing.T) {
		tests := []struct {
			name       string
			pluginName string
			wantErr    error
		}{
			{name: "the name matches", pluginName: "echo"},
			{name: "a mismatched name fails", pluginName: "acme", wantErr: ErrNameMismatch},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				client, launchErr := Launch(t.Context(), tt.pluginName, Spec{Cmd: bin, Dir: t.TempDir()})
				require.NoError(t, launchErr)
				t.Cleanup(client.Close)

				info, infoErr := client.Info(t.Context())
				if tt.wantErr != nil {
					require.ErrorIs(t, infoErr, tt.wantErr)
					assert.Contains(t, infoErr.Error(), tt.pluginName,
						"the error must carry the configured name so the failure is traceable")
					return
				}
				require.NoError(t, infoErr)
				assert.Equal(t, "echo", info.Name)

				names := make([]string, len(info.Steps))
				for i, st := range info.Steps {
					names[i] = st.Name
				}
				assert.Contains(t, names, "echo", "the provider must report the step types it offers")
				assert.Contains(t, names, "fail", "the provider must report every step type it offers")
			})
		}
	})

	t.Run("reports the provider icon", func(t *testing.T) {
		client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
		require.NoError(t, err)
		t.Cleanup(client.Close)

		info, err := client.Info(t.Context())
		require.NoError(t, err)
		assert.NotEmpty(t, info.Icon, "the echo plugin ships a demo icon; Info must decode it")
	})

	t.Run("reports the echo step type's tools", func(t *testing.T) {
		client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
		require.NoError(t, err)
		t.Cleanup(client.Close)

		info, err := client.Info(t.Context())
		require.NoError(t, err)

		var echoStep StepInfo
		for _, st := range info.Steps {
			if st.Name == "echo" {
				echoStep = st
			}
		}
		require.Len(t, echoStep.Tools, 1)
		assert.Equal(t, "echo", echoStep.Tools[0].Name)
		assert.NotEmpty(t, echoStep.Tools[0].InputSchema)
	})
}

func TestConfigure(t *testing.T) {
	bin := buildEchoPlugin(t)

	tests := []struct {
		name   string
		config []byte
	}{
		{name: "delivers the config block", config: []byte(`{"greeting":"hi"}`)},
		{name: "accepts an empty config block", config: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, launchErr := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
			require.NoError(t, launchErr)
			t.Cleanup(client.Close)

			require.NoError(t, client.Configure(t.Context(), tt.config, nil))
		})
	}

	t.Run("reports an error once the connection is gone", func(t *testing.T) {
		client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
		require.NoError(t, err)
		client.Close()

		err = client.Configure(t.Context(), nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Configure")
	})
}

func TestUp(t *testing.T) {
	bin := buildEchoPlugin(t)
	client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	t.Run("returns the published result and streams events", func(t *testing.T) {
		var events []*pb.Event
		result, err := client.Up(t.Context(), &pb.UpRequest{
			Step:   "api",
			Type:   "echo",
			Config: []byte(`{"message":"hello"}`),
		}, func(ev *pb.Event) { events = append(events, ev) })
		require.NoError(t, err)

		assert.Equal(t, "api", result.GetOutputs().GetValues()["step"].GetStringValue())
		require.Len(t, events, 1, "the message log line must reach onEvent")
		assert.Equal(t, "hello", events[0].GetLog().GetText())
	})

	t.Run("reports the step's error", func(t *testing.T) {
		_, err := client.Up(t.Context(), &pb.UpRequest{Step: "boom", Type: "fail"}, func(*pb.Event) {})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("carries the step's human-facing message across the plugin process boundary", func(t *testing.T) {
		_, err := client.Up(t.Context(), &pb.UpRequest{Step: "boom", Type: "fail"}, func(*pb.Event) {})
		require.Error(t, err)
		assert.Equal(t, "the fail step always fails, on purpose", uerr.Display(err))
	})
}

func TestExport(t *testing.T) {
	bin := buildEchoPlugin(t)
	client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	t.Run("reports Env and Out separately", func(t *testing.T) {
		resp, exportErr := client.Export(t.Context(), &pb.ExportRequest{
			Step: "api", Type: "echo",
			Config: []byte(`{"export":{"greeting":"hi","password":"hunter2"},"export_sensitive":["password"]}`),
		})
		require.NoError(t, exportErr)
		assert.Equal(t, map[string]string{"greeting": "hi", "password": "hunter2"}, resp.GetEnv(),
			"echo's Export populates Env from the same export map as Out")
		values := resp.GetOut().GetValues()
		require.Contains(t, values, "greeting")
		assert.Equal(t, "hi", values["greeting"].GetStringValue())
		assert.False(t, values["greeting"].GetSensitive())
		require.Contains(t, values, "password")
		assert.Equal(t, "hunter2", values["password"].GetStringValue())
		assert.True(t, values["password"].GetSensitive(), "export_sensitive must mark the value Sensitive")
	})

	t.Run("a step type with no Exporter fails at the plugin, not pluginhost", func(t *testing.T) {
		// The probe step type does not implement Exporter, so the plugin
		// itself - not pluginhost - reports the failure.
		_, exportErr := client.Export(t.Context(), &pb.ExportRequest{Step: "api", Type: "probe"})
		require.Error(t, exportErr)
		assert.Equal(t, codes.Unimplemented, status.Code(exportErr))
	})
}

func TestCallTool(t *testing.T) {
	bin := buildEchoPlugin(t)
	client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	t.Run("runs the tool and returns its content", func(t *testing.T) {
		resp, callErr := client.CallTool(t.Context(), &pb.ToolCallRequest{
			Step: "api", Type: "echo", Tool: "echo",
			Config:    []byte(`{"message":"hi"}`),
			Arguments: []byte(`{"n":1}`),
		})
		require.NoError(t, callErr)
		assert.False(t, resp.GetIsError())
		assert.JSONEq(t, `{"message":"hi","arguments":{"n":1},"deps":null}`, string(resp.GetContent()))
	})

	t.Run("a tool-reported failure sets IsError, not an RPC error", func(t *testing.T) {
		resp, callErr := client.CallTool(t.Context(), &pb.ToolCallRequest{
			Step: "api", Type: "echo", Tool: "echo", Arguments: []byte(`{"fail":true}`),
		})
		require.NoError(t, callErr)
		assert.True(t, resp.GetIsError())
		assert.Equal(t, "requested failure", resp.GetErrorMessage())
	})

	t.Run("an unknown tool name fails at the plugin, not pluginhost", func(t *testing.T) {
		_, callErr := client.CallTool(t.Context(), &pb.ToolCallRequest{Step: "api", Type: "echo", Tool: "no-such-tool"})
		require.Error(t, callErr)
		assert.Contains(t, callErr.Error(), "no-such-tool")
	})

	t.Run("a step type with no ToolProvider fails at the plugin, not pluginhost", func(t *testing.T) {
		// probe implements neither Tools nor CallTool, so the plugin
		// itself - not pluginhost - reports the failure.
		_, callErr := client.CallTool(t.Context(), &pb.ToolCallRequest{Step: "api", Type: "probe", Tool: "echo"})
		require.Error(t, callErr)
		assert.Equal(t, codes.Unimplemented, status.Code(callErr))
	})
}

func TestDown(t *testing.T) {
	bin := buildEchoPlugin(t)
	client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	t.Run("streams the removal log", func(t *testing.T) {
		var events []*pb.Event
		err := client.Down(t.Context(), &pb.DownRequest{Step: "api", Type: "echo"}, func(ev *pb.Event) { events = append(events, ev) })
		require.NoError(t, err)

		require.Len(t, events, 1)
		assert.Contains(t, events[0].GetLog().GetText(), "removing api")
	})

	t.Run("reports a step type that does not implement Down", func(t *testing.T) {
		// failStep has no Down method.
		err := client.Down(t.Context(), &pb.DownRequest{Step: "boom", Type: "fail"}, func(*pb.Event) {})
		require.Error(t, err)
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})
}

func TestACrashedPluginReportsErrCrashed(t *testing.T) {
	bin := buildEchoPlugin(t)

	client, err := Launch(t.Context(), "echo", Spec{Cmd: bin, Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	// Confirm the plugin actually answers before taking it down.
	_, err = client.Info(t.Context())
	require.NoError(t, err, "success before crash")

	reattach := client.client.ReattachConfig()
	require.NotNil(t, reattach, "no PID to kill: ReattachConfig reported none")
	require.NotZero(t, reattach.Pid, "no PID to kill: ReattachConfig reported none")

	proc, err := os.FindProcess(reattach.Pid)
	require.NoError(t, err, "find the plugin process")
	require.NoError(t, proc.Kill(), "kill the plugin process")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, err = client.Info(ctx)
		if err != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the plugin never reported an error after its process was killed")
		case <-time.After(50 * time.Millisecond):
			// poll
		}
	}
	assert.ErrorIs(t, err, ErrCrashed)
}

func TestWrapRPCErr(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCrashed bool
	}{
		{
			name:        "marks Unavailable as crashed",
			err:         status.Error(codes.Unavailable, "transport is closing"),
			wantCrashed: true,
		},
		{name: "leaves a plain error alone", err: errors.New("plain error")},
		{name: "leaves another grpc code alone", err: status.Error(codes.InvalidArgument, "bad request")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantCrashed {
				assert.ErrorIs(t, wrapRPCErr(tt.err), ErrCrashed)
				return
			}
			assert.NotErrorIs(t, wrapRPCErr(tt.err), ErrCrashed)
		})
	}

	t.Run("reconstructs a human-facing message carried as a status detail", func(t *testing.T) {
		st, err := status.New(codes.Unknown, "boom").
			WithDetails(&pb.UserMessage{Key: "no %s in stock", Args: []string{"widgets"}})
		require.NoError(t, err)

		got := wrapRPCErr(st.Err())
		assert.Equal(t, "no widgets in stock", uerr.Display(got))

		var ue *uerr.Error
		require.ErrorAs(t, got, &ue)
		assert.Equal(t, "no %s in stock", ue.Format())
		assert.Equal(t, []string{"widgets"}, ue.Args())
	})
}

func TestErrorsAreSentinels(t *testing.T) {
	assert.Contains(t, ErrNoResult.Error(), "pluginhost")
	assert.Contains(t, ErrNameMismatch.Error(), "pluginhost")
}
