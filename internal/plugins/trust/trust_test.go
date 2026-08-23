package trust

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	trustinstall "github.com/justenwalker/kevin/internal/trust"
)

func TestStepIsIdempotent(t *testing.T) {
	assert.True(t, Step{}.Idempotent(), "a store that already matches the desired state is success, not an error")
}

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Step{}.Schema()

	assert.Contains(t, string(schema), "#Config")
	assert.Contains(t, string(schema), "system")
	assert.Contains(t, string(schema), "firefox")
}

func TestDecode(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)

		assert.False(t, cfg.System, "the store of the user needs no root, thus it is the default")
		assert.True(t, cfg.Firefox)
	})

	t.Run("reads every field", func(t *testing.T) {
		cfg, err := decode([]byte(`{"system":true,"firefox":false}`))
		require.NoError(t, err)

		assert.True(t, cfg.System)
		assert.False(t, cfg.Firefox)
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte(`{`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode config")
	})
}

func TestRequestForPointsAtTheRootNotTheProject(t *testing.T) {
	one := requestFor(config{System: true, Firefox: true})

	assert.Equal(t, ca.RootCertPath(), one.CertPath)
	assert.Equal(t, ca.RootCommonName, one.CommonName)
	assert.Equal(t, "kevin-local-root", one.FileName)
	assert.True(t, one.System)
	assert.True(t, one.Firefox)

	// Every project asks for the same entry, thus a trust store holds one
	// kevin anchor however many projects exist.
	two := requestFor(config{})
	assert.Equal(t, one.CertPath, two.CertPath)
	assert.Equal(t, one.CommonName, two.CommonName)
	assert.Equal(t, one.FileName, two.FileName)
}

func TestReport(t *testing.T) {
	out := &capture{}

	report(out, []trustinstall.Result{
		{Store: "macos-user", Installed: true},
		{Store: "firefox", Installed: true, Reason: "2 profiles"},
		{Store: "linux-system", Skipped: true, Reason: "no anchor directory"},
		{Store: "macos-user"},
	})

	assert.Equal(t, []string{
		"macos-user: installed",
		"firefox: installed (2 profiles)",
		"linux-system: skipped, no anchor directory",
		"macos-user: removed",
	}, out.logs)
}

func TestAdvise(t *testing.T) {
	t.Run("names the command for a store that needs root", func(t *testing.T) {
		err := advise(trustinstall.ErrNeedsRoot, []trustinstall.Result{
			{Store: "macos-system", Reason: "run: sudo security add-trusted-cert ..."},
		})

		require.ErrorIs(t, err, trustinstall.ErrNeedsRoot)
		assert.Contains(t, err.Error(), "sudo security add-trusted-cert",
			"the plugin must never ask for a password itself, thus it prints the command")
	})

	t.Run("leaves any other error alone", func(t *testing.T) {
		err := advise(assert.AnError, []trustinstall.Result{{Reason: "irrelevant"}})

		require.ErrorIs(t, err, assert.AnError)
		assert.NotContains(t, err.Error(), "irrelevant")
	})
}

type capture struct{ logs []string }

func (c *capture) Log(_, text string)            { c.logs = append(c.logs, text) }
func (c *capture) Progress(string, int64, int64) {}
