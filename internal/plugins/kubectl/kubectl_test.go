package kubectl

import (
	"encoding/json"
	"path/filepath"
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

func TestDecode(t *testing.T) {
	t.Run("empty config", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)
		assert.False(t, cfg.ServerSide, "server_side must default to false")
		assert.False(t, cfg.Keep, "keep must default to false")
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte("{"))
		assert.Error(t, err, "expected an error for malformed JSON")
	})
}

func TestValidateSource(t *testing.T) {
	t.Run("rejects zero", func(t *testing.T) {
		require.ErrorIs(t, validateSource(config{}), ErrSource)
	})

	t.Run("rejects multiple", func(t *testing.T) {
		require.ErrorIs(t, validateSource(config{Manifest: "a", Path: "b"}), ErrSource)
	})

	t.Run("accepts exactly one", func(t *testing.T) {
		assert.NoError(t, validateSource(config{Manifest: "a"}))
	})
}

func TestUpRejectsZeroSources(t *testing.T) {
	_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Config: json.RawMessage(`{"kubeconfig":"/tmp/kubeconfig"}`),
	}, &capture{})
	assert.ErrorIs(t, err, ErrSource)
}

func TestResolvePath(t *testing.T) {
	cases := []struct {
		name, path, projectDir, want string
	}{
		{name: "empty path passes through", path: "", projectDir: "/proj", want: ""},
		{name: "absolute path is untouched", path: "/abs/path", projectDir: "/proj", want: "/abs/path"},
		{name: "no project dir passes through", path: "rel/path", projectDir: "", want: "rel/path"},
		{name: "relative path joins the project dir", path: "rel/path", projectDir: "/proj", want: filepath.Join("/proj", "rel/path")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, resolvePath(c.path, c.projectDir))
		})
	}
}

func TestKindIsAction(t *testing.T) {
	assert.Equal(t, plugin.StepKindAction, Step{}.Kind(), "a kubectl step is apply-only")
}

func TestStepImplementsDowner(t *testing.T) {
	_, ok := any(Step{}).(plugin.Downer)
	assert.True(t, ok, "a kubectl step must delete what it applied on teardown")
}

func TestStepIsIdempotent(t *testing.T) {
	assert.True(t, Step{}.Idempotent(), "kubectl apply reconciles the cluster rather than erroring on a rerun")
}

func TestDownRejectsZeroSources(t *testing.T) {
	err := Step{}.Down(t.Context(), &plugin.DownRequest{
		Config: json.RawMessage(`{"kubeconfig":"/tmp/kubeconfig"}`),
	}, &capture{})
	assert.ErrorIs(t, err, ErrSource)
}

func TestDownKeepSkipsDelete(t *testing.T) {
	out := &capture{}
	err := Step{}.Down(t.Context(), &plugin.DownRequest{
		Config: json.RawMessage(`{"kubeconfig":"/tmp/kubeconfig","manifest":"apiVersion: v1\n","keep":true}`),
	}, out)
	require.NoError(t, err)
	assert.Contains(t, out.logs, "keeping inline manifest")
}
