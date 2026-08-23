package helm

import (
	"encoding/json"
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
	t.Run("defaults", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)
		assert.Equal(t, "default", cfg.Namespace)
		assert.True(t, cfg.CreateNamespace, "create_namespace must default to true")
		assert.Equal(t, "5m", cfg.Wait)
		assert.True(t, cfg.Atomic, "atomic must default to true")
		assert.False(t, cfg.Keep, "keep must default to false")
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte("{"))
		assert.Error(t, err, "expected an error for malformed JSON")
	})
}

func TestResolveChart(t *testing.T) {
	cases := []struct {
		name, chart, repo, projectDir, want string
	}{
		{name: "relative path joins the project dir", chart: "./charts/demo", repo: "", projectDir: "/proj", want: "/proj/charts/demo"},
		{name: "a repo chart name is untouched", chart: "nginx", repo: "https://charts.example.com", projectDir: "/proj", want: "nginx"},
		{name: "an oci reference is untouched", chart: "oci://example.com/nginx", repo: "", projectDir: "/proj", want: "oci://example.com/nginx"},
		{name: "an absolute path is untouched", chart: "/abs/chart", repo: "", projectDir: "/proj", want: "/abs/chart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, resolveChart(c.chart, c.repo, c.projectDir))
		})
	}
}

func TestParseWait(t *testing.T) {
	t.Run("empty disables wait", func(t *testing.T) {
		d, err := parseDuration("")
		require.NoError(t, err)
		assert.Zero(t, d)
	})

	t.Run("rejects a bad duration", func(t *testing.T) {
		_, err := parseDuration("not-a-duration")
		assert.Error(t, err, "expected an error for a malformed duration")
	})
}

func TestKindIsAction(t *testing.T) {
	assert.Equal(t, plugin.StepKindAction, Step{}.Kind(), "a helm step is apply-only")
}

func TestStepImplementsDowner(t *testing.T) {
	_, ok := any(Step{}).(plugin.Downer)
	assert.True(t, ok, "a helm step must uninstall its release on teardown")
}

func TestStepIsIdempotent(t *testing.T) {
	assert.True(t, Step{}.Idempotent(), "helm upgrade --install reconciles the release rather than erroring on a rerun")
}

func TestDownKeepSkipsUninstall(t *testing.T) {
	cfg, err := json.Marshal(map[string]any{
		"release": "demo",
		"keep":    true,
	})
	require.NoError(t, err)

	out := &capture{}
	err = Step{}.Down(t.Context(), &plugin.DownRequest{Config: cfg}, out)
	require.NoError(t, err)
	assert.Contains(t, out.logs, "keeping release demo")
}
