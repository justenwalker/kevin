package helm

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	helmcmd "github.com/justenwalker/kevin/internal/command/helm"
	"github.com/justenwalker/kevin/internal/plugins/plugintest"
	"github.com/justenwalker/kevin/plugin"
)

type capture struct{ logs []string }

func (c *capture) Log(_, text string)            { c.logs = append(c.logs, text) }
func (c *capture) Progress(string, int64, int64) {}

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	assert.NotEmpty(t, schema, "Schema must return the embedded schema.cue")
}

func TestDecode(t *testing.T) {
	t.Run("a with block carries the schema's defaults", func(t *testing.T) {
		cfg, err := decode(withDefaults(t, `{"kubeconfig":"/kc","chart":"c","release":"r"}`))
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
	_, ok := any(Step{}).(plugin.TypedDowner[config])
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
	err = typed(Step{}).Down(t.Context(), &plugin.DownRequest{Config: cfg}, out)
	require.NoError(t, err)
	assert.Contains(t, out.logs, "keeping release demo")
}

func TestUp(t *testing.T) {
	cfg := withDefaults(t, `{"kubeconfig":"/kc","release":"demo","chart":"./charts/demo"}`)
	req := &plugin.UpRequest{Config: cfg, Env: plugin.Env{ProjectDir: "/proj"}}

	t.Run("installs the release and publishes outputs", func(t *testing.T) {
		cli := newMockcli(t)
		cli.EXPECT().UpgradeInstall(mock.Anything, mock.MatchedBy(func(spec helmcmd.UpgradeSpec) bool {
			return spec.Release == "demo" && spec.Chart == "/proj/charts/demo" && spec.Kubeconfig == "/kc"
		})).Return("", nil)

		res, err := typed(Step{cli: cli}).Up(t.Context(), req, &capture{})
		require.NoError(t, err)
		assert.Equal(t, plugin.StringMap(map[string]string{
			"release": "demo", "namespace": "default", "kubeconfig": "/kc", "context": "",
		}), res.Outputs)
	})

	t.Run("wraps a helm failure", func(t *testing.T) {
		cli := newMockcli(t)
		cli.EXPECT().UpgradeInstall(mock.Anything, mock.Anything).Return("", errors.New("boom"))

		_, err := typed(Step{cli: cli}).Up(t.Context(), req, &capture{})
		require.ErrorContains(t, err, "helm: boom")
	})
}

func TestDown(t *testing.T) {
	cfg := withDefaults(t, `{"kubeconfig":"/kc","release":"demo","chart":"c"}`)
	req := &plugin.DownRequest{Config: cfg}

	t.Run("uninstalls the release", func(t *testing.T) {
		cli := newMockcli(t)
		cli.EXPECT().Uninstall(mock.Anything, helmcmd.UninstallSpec{
			Kubeconfig: "/kc", Release: "demo", Namespace: "default",
		}).Return("", nil)

		out := &capture{}
		require.NoError(t, typed(Step{cli: cli}).Down(t.Context(), req, out))
		assert.Contains(t, out.logs, "release demo uninstalled")
	})

	t.Run("treats a missing release as done", func(t *testing.T) {
		cli := newMockcli(t)
		cli.EXPECT().Uninstall(mock.Anything, mock.Anything).Return("", helmcmd.ErrReleaseNotFound)

		out := &capture{}
		require.NoError(t, typed(Step{cli: cli}).Down(t.Context(), req, out))
		assert.Contains(t, out.logs, "release demo already gone")
	})

	t.Run("reports any other failure", func(t *testing.T) {
		cli := newMockcli(t)
		cli.EXPECT().Uninstall(mock.Anything, mock.Anything).Return("", errors.New("boom"))

		require.ErrorContains(t, typed(Step{cli: cli}).Down(t.Context(), req, &capture{}), "helm: boom")
	})
}

// withDefaults feeds a with block to the plugin the way the engine does.
func withDefaults(t *testing.T, with string) []byte {
	t.Helper()
	return plugintest.Defaulted(t, schema, with)
}

// stepAll is every optional part the SDK's typed adapter offers, so a test
// can call it with a raw with block.
type stepAll interface {
	plugin.Step
	plugin.Downer
	plugin.Exporter
}

// typed wraps s the way New does.
func typed(s Step) stepAll { return plugin.Typed(schema, s).(stepAll) } //nolint:errcheck,ireturn // the adapter implements every part

// decode parses a with block the way the SDK does for a typed step.
func decode(data []byte) (config, error) {
	var cfg config
	if len(data) == 0 {
		return cfg, nil
	}
	err := json.Unmarshal(data, &cfg)
	return cfg, err
}
