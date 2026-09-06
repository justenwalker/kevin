package route

import (
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Step{}.Schema()

	assert.Contains(t, string(schema), "#Config")
	assert.Contains(t, string(schema), "relay")
	assert.Contains(t, string(schema), "routes")
}

func TestKindIsAction(t *testing.T) {
	assert.Equal(t, plugin.StepKindAction, Step{}.Kind(), "a route step registers routes but owns no lifecycle")
}

func TestStepDoesNotImplementDowner(t *testing.T) {
	_, ok := any(Step{}).(plugin.Downer)
	assert.False(t, ok, "a route step has nothing to clean up on teardown")
}

func TestStepIsIdempotent(t *testing.T) {
	assert.True(t, Step{}.Idempotent(), "the proxy's route table replaces a route for the same host, so Up is safe to call again")
}

func TestDecode(t *testing.T) {
	t.Run("empty config", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)
		assert.Empty(t, cfg.Relay)
		assert.Empty(t, cfg.Routes)
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte("{"))
		assert.Error(t, err, "expected an error for malformed JSON")
	})
}

func TestUpBuildsARouteAndDetailPerEntry(t *testing.T) {
	result, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step: "myapp_route",
		Env:  plugin.Env{Domain: "kevin.home"},
		Config: []byte(`{
			"relay": "127.0.0.1:54321",
			"routes": [
				{"host": "myapp", "address": "myapp.default.svc.cluster.local:80"},
				{"host": "other", "address": "other.default.svc.cluster.local:8443", "tls": true},
				{"host": "certmanager", "address": "certmanager.default.svc.cluster.local:8443", "tls": true, "mode": "passthrough"}
			]
		}`),
	}, &noopEmitter{})
	require.NoError(t, err)

	require.Len(t, result.Routes, 3)
	assert.Equal(t, plugin.Route{
		Host:     "myapp.kevin.home",
		Upstream: "socks5://127.0.0.1:54321/myapp.default.svc.cluster.local:80",
	}, result.Routes[0])
	assert.Equal(t, plugin.Route{
		Host:     "other.kevin.home",
		Upstream: "socks5://127.0.0.1:54321/other.default.svc.cluster.local:8443",
		TLS:      true,
	}, result.Routes[1])
	assert.Equal(t, plugin.Route{
		Host:     "certmanager.kevin.home",
		Upstream: "socks5://127.0.0.1:54321/certmanager.default.svc.cluster.local:8443",
		TLS:      true,
		Mode:     plugin.RouteModePassthrough,
	}, result.Routes[2])

	require.Len(t, result.Details, 3)
	assert.Equal(t, result.Routes[0].Detail(), result.Details[0])
	assert.Equal(t, result.Routes[1].Detail(), result.Details[1])
	assert.Equal(t, result.Routes[2].Detail(), result.Details[2])
}

func TestUpWithInterceptSkipsTheDomainSuffix(t *testing.T) {
	result, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step: "s3_intercept",
		Env:  plugin.Env{Domain: "kevin.home"},
		Config: []byte(`{
			"routes": [
				{"host": "s3.amazonaws.com", "address": "127.0.0.1:9090", "intercept": true, "ports": [443]},
				{"host": "*.s3.amazonaws.com", "address": "127.0.0.1:9090", "tls": true, "intercept": true, "ports": [443]}
			]
		}`),
	}, &noopEmitter{})
	require.NoError(t, err)

	require.Len(t, result.Routes, 2)
	assert.Equal(t, plugin.Route{
		Host:      "s3.amazonaws.com",
		Upstream:  "127.0.0.1:9090",
		Intercept: &plugin.RouteIntercept{Ports: []int{443}},
	}, result.Routes[0], "intercept must use the host verbatim, not suffixed with the environment domain")
	assert.Equal(t, plugin.Route{
		Host:      "*.s3.amazonaws.com",
		Upstream:  "127.0.0.1:9090",
		TLS:       true,
		Intercept: &plugin.RouteIntercept{Ports: []int{443}},
	}, result.Routes[1])
}

func TestUpWithAWildcardHostSuffixesTheDomainBeforeTheProxySeesIt(t *testing.T) {
	// The proxy's route table treats any Route.Host with a leading "*." as
	// a wildcard, regardless of intercept - it never sees "intercept" at
	// all, only the final Host string. A non-intercept "*.foo" must still
	// get the domain suffix appended after the wildcard marker, not
	// instead of it, so "*.foo.kevin.home" reaches the table intact.
	result, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step: "tenant_route",
		Env:  plugin.Env{Domain: "kevin.home"},
		Config: []byte(`{
			"routes": [
				{"host": "*.foo", "address": "127.0.0.1:9090"}
			]
		}`),
	}, &noopEmitter{})
	require.NoError(t, err)

	require.Len(t, result.Routes, 1)
	assert.Equal(t, plugin.Route{
		Host:     "*.foo.kevin.home",
		Upstream: "127.0.0.1:9090",
	}, result.Routes[0], "a wildcard host must keep its leading *. through the domain suffix, matching anything.foo.kevin.home")
}

func TestSchemaMode(t *testing.T) {
	ctx := cuecontext.New()
	v := ctx.CompileBytes(Step{}.Schema(), cue.Filename("route/schema.cue"))
	require.NoError(t, v.Err())
	route := v.LookupPath(cue.ParsePath("#Route"))

	t.Run("defaults to mitm", func(t *testing.T) {
		with := ctx.CompileString(`{host: "a", address: "b:1"}`)
		merged := route.Unify(with)
		require.NoError(t, merged.Err())
		mode, err := merged.LookupPath(cue.ParsePath("mode")).String()
		require.NoError(t, err)
		assert.Equal(t, "mitm", mode)
	})

	t.Run("passthrough alone fills tls: true", func(t *testing.T) {
		with := ctx.CompileString(`{host: "a", address: "b:1", mode: "passthrough"}`)
		merged := route.Unify(with)
		require.NoError(t, merged.Err())
		tls, err := merged.LookupPath(cue.ParsePath("tls")).Bool()
		require.NoError(t, err)
		assert.True(t, tls, "passthrough implies tls: true")
	})

	t.Run("passthrough with tls false conflicts", func(t *testing.T) {
		with := ctx.CompileString(`{host: "a", address: "b:1", tls: false, mode: "passthrough"}`)
		merged := route.Unify(with)
		assert.Error(t, merged.Err(), "a plain-HTTP target has no certificate to preserve")
	})

	t.Run("raw alone fills tls: false", func(t *testing.T) {
		with := ctx.CompileString(`{host: "a", address: "b:1", mode: "raw"}`)
		merged := route.Unify(with)
		require.NoError(t, merged.Err())
		tls, err := merged.LookupPath(cue.ParsePath("tls")).Bool()
		require.NoError(t, err)
		assert.False(t, tls, "raw has no TLS layer at all")
	})

	t.Run("raw with tls true conflicts", func(t *testing.T) {
		with := ctx.CompileString(`{host: "a", address: "b:1", tls: true, mode: "raw"}`)
		merged := route.Unify(with)
		assert.Error(t, merged.Err(), "a raw target has no TLS layer to negotiate")
	})

	t.Run("tls alone needs no mode change", func(t *testing.T) {
		with := ctx.CompileString(`{host: "a", address: "b:1", tls: true}`)
		merged := route.Unify(with)
		require.NoError(t, merged.Err())
		mode, err := merged.LookupPath(cue.ParsePath("mode")).String()
		require.NoError(t, err)
		assert.Equal(t, "mitm", mode)
	})

	t.Run("an unrecognized mode is rejected", func(t *testing.T) {
		with := ctx.CompileString(`{host: "a", address: "b:1", mode: "bogus"}`)
		merged := route.Unify(with)
		assert.Error(t, merged.Err())
	})
}

func TestParseRouteMode(t *testing.T) {
	tests := []struct {
		mode string
		want plugin.RouteMode
	}{
		{mode: "", want: plugin.RouteModeMITM},
		{mode: "mitm", want: plugin.RouteModeMITM},
		{mode: "passthrough", want: plugin.RouteModePassthrough},
		{mode: "raw", want: plugin.RouteModeRaw},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			assert.Equal(t, tt.want, parseRouteMode(tt.mode))
		})
	}
}

type noopEmitter struct{}

func (noopEmitter) Log(string, string)            {}
func (noopEmitter) Progress(string, int64, int64) {}
