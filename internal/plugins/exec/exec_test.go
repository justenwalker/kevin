package exec

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

type capture struct{ logs []string }

func (c *capture) Log(_, text string)            { c.logs = append(c.logs, text) }
func (c *capture) Progress(string, int64, int64) {}

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	assert.NotEmpty(t, Step{}.Schema(), "Schema must return the embedded schema.cue")
}

func TestKind(t *testing.T) {
	assert.Equal(t, plugin.StepKindAction, Step{}.Kind())
}

func TestDecode(t *testing.T) {
	t.Run("decodes up and leaves down nil when omitted", func(t *testing.T) {
		cfg, err := decode([]byte(`{"up":{"command":["echo","hi"]}}`))
		require.NoError(t, err)
		assert.Equal(t, []string{"echo", "hi"}, cfg.Up.Command)
		assert.Nil(t, cfg.Down, "an omitted down must decode as nil, not a zero-value #Exec")
		assert.False(t, cfg.Proxy, "proxy must default to false")
	})

	t.Run("decodes down when present", func(t *testing.T) {
		cfg, err := decode([]byte(`{"up":{"command":["echo","hi"]},"down":{"command":["echo","bye"]}}`))
		require.NoError(t, err)
		require.NotNil(t, cfg.Down)
		assert.Equal(t, []string{"echo", "bye"}, cfg.Down.Command)
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte("{"))
		assert.Error(t, err)
	})
}

func TestWorkDir(t *testing.T) {
	t.Run("empty cwd uses the project directory", func(t *testing.T) {
		assert.Equal(t, "/project", workDir("", "/project"))
	})

	t.Run("relative cwd resolves against the project directory", func(t *testing.T) {
		assert.Equal(t, filepath.Join("/project", "sub"), workDir("sub", "/project"))
	})

	t.Run("absolute cwd is used as-is", func(t *testing.T) {
		assert.Equal(t, "/elsewhere", workDir("/elsewhere", "/project"))
	})
}

func TestBuildEnv(t *testing.T) {
	t.Run("proxy off adds nothing beyond the host environment and the command's own env", func(t *testing.T) {
		result := buildEnv(execConfig{Env: map[string]string{"FOO": "bar"}}, false, plugin.Env{
			HTTPProxyAddr: "127.0.0.1:8080",
			CAPath:        "/ca.crt",
		})
		m := envMap(result)
		assert.Equal(t, "bar", m["FOO"])
		assert.NotContains(t, m, "HTTP_PROXY")
		assert.NotContains(t, m, "SSL_CERT_FILE")
	})

	t.Run("proxy on builds HTTP_PROXY/HTTPS_PROXY from HTTPProxyAddr, not ProxyEnv", func(t *testing.T) {
		result := buildEnv(execConfig{}, true, plugin.Env{
			HTTPProxyAddr: "127.0.0.1:8080",
			CAPath:        "/ca.crt",
			// ProxyEnv is the docker-network-gateway-oriented map a container
			// uses. A host-executed command must not pick this up.
			ProxyEnv: map[string]string{"HTTP_PROXY": "http://host.docker.internal:9999"},
		})
		m := envMap(result)
		assert.Equal(t, "http://127.0.0.1:8080", m["HTTP_PROXY"])
		assert.Equal(t, "http://127.0.0.1:8080", m["HTTPS_PROXY"])
		assert.Equal(t, "http://127.0.0.1:8080", m["http_proxy"])
		assert.Equal(t, "http://127.0.0.1:8080", m["https_proxy"])
		assert.Equal(t, "/ca.crt", m["SSL_CERT_FILE"])
	})

	t.Run("an explicit env entry takes precedence over a proxy default", func(t *testing.T) {
		result := buildEnv(execConfig{Env: map[string]string{"HTTP_PROXY": "http://explicit:1"}}, true, plugin.Env{
			HTTPProxyAddr: "127.0.0.1:8080",
		})
		m := envMap(result)
		assert.Equal(t, "http://explicit:1", m["HTTP_PROXY"])
	})
}

// envMap turns the []string result of buildEnv into a map, keeping only
// the last value per key, the same rule os/exec applies to cmd.Env.
func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestUp(t *testing.T) {
	t.Run("captures stdout as an output and runs in the project directory", func(t *testing.T) {
		dir := t.TempDir()
		req := &plugin.UpRequest{
			Env:    plugin.Env{ProjectDir: dir},
			Config: json.RawMessage(`{"up":{"command":["sh","-c","echo hello; pwd"]}}`),
		}
		result, err := Step{}.Up(t.Context(), req, &capture{})
		require.NoError(t, err)

		stdout, ok := result.Outputs["stdout"]
		require.True(t, ok)
		lines := strings.Split(stdout.String(), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, "hello", lines[0])

		wantDir, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)
		gotDir, err := filepath.EvalSymlinks(lines[1])
		require.NoError(t, err)
		assert.Equal(t, wantDir, gotDir)
	})

	t.Run("a nonzero exit surfaces stderr", func(t *testing.T) {
		req := &plugin.UpRequest{
			Config: json.RawMessage(`{"up":{"command":["sh","-c","echo boom >&2; exit 1"]}}`),
		}
		_, err := Step{}.Up(t.Context(), req, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestDown(t *testing.T) {
	t.Run("does nothing when down is unset", func(t *testing.T) {
		req := &plugin.DownRequest{
			Config: json.RawMessage(`{"up":{"command":["echo","hi"]}}`),
		}
		err := Step{}.Down(t.Context(), req, &capture{})
		assert.NoError(t, err)
	})

	t.Run("runs down's own command with its own cwd", func(t *testing.T) {
		upDir := t.TempDir()
		downDir := t.TempDir()
		req := &plugin.DownRequest{
			Env: plugin.Env{ProjectDir: "/should-not-be-used"},
			Config: json.RawMessage(`{
				"up":   {"command": ["echo", "hi"], "cwd": "` + upDir + `"},
				"down": {"command": ["sh", "-c", "touch marker"], "cwd": "` + downDir + `"}
			}`),
		}
		err := Step{}.Down(t.Context(), req, &capture{})
		require.NoError(t, err)

		assert.FileExists(t, filepath.Join(downDir, "marker"), "down must run in its own cwd, not up's")
		assert.NoFileExists(t, filepath.Join(upDir, "marker"))
	})
}
