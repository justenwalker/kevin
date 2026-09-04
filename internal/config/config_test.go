package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

// listenBlockDefault is a "proxy: {...}, console: {...}" CUE snippet
// naming arbitrary literal ports as defaults, not concrete values -
// proxy.listen/proxy.gateway_port/console.listen carry no schema default,
// but nothing in this package binds a real port (pure CUE decode, no
// network), so a fixed literal default is enough. A src that sets its own
// value for any of these three fields unifies its concrete value over
// this default instead of conflicting with it.
const listenBlockDefault = `proxy: {listen: string | *"127.0.0.1:18080", gateway_port: int | *18081}
console: listen: string | *"127.0.0.1:18082"
`

func TestLoad(t *testing.T) {
	t.Run("reports a missing file", func(t *testing.T) {
		_, err := config.Load(t.TempDir(), "", nil)
		require.ErrorIs(t, err, config.ErrNotFound)
	})

	t.Run("reports a read error that isn't a missing file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "kevin.cue"), 0o750), "kevin.cue as a directory forces a read error")

		_, err := config.Load(dir, "", nil)
		require.Error(t, err)
		require.NotErrorIs(t, err, config.ErrNotFound)
	})

	t.Run("reports a malformed package clause", func(t *testing.T) {
		dir := write(t, "package 123\nproject: \"base\"")
		_, err := config.Load(dir, "", nil)
		require.Error(t, err)
	})

	t.Run("resolves a relative directory", func(t *testing.T) {
		dir := write(t, `project: "rel"`)
		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(f.Dir()), "Dir must be absolute")
	})

	t.Run("resolves a named environment", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.cue"), []byte(listenBlockDefault+`project: "staging-env"`), 0o600))

		f, err := config.Load(dir, "staging", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "staging-env", cfg.Project)
	})

	t.Run("resolves a dotfile-named environment", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".staging.kevin.yaml"),
			[]byte("project: staging-hidden\nproxy:\n  listen: \"127.0.0.1:18080\"\n  gateway_port: 18081\nconsole:\n  listen: \"127.0.0.1:18082\"\n"), 0o600))

		f, err := config.Load(dir, "staging", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "staging-hidden", cfg.Project)
	})

	t.Run("resolves yaml and json the same as an equivalent cue file", func(t *testing.T) {
		cueDir := write(t, `project: "from-yaml"
plugins: echo: cmd: "echo"`)
		cf, err := config.Load(cueDir, "", nil)
		require.NoError(t, err)
		cueSpecs, err := cf.Plugins()
		require.NoError(t, err)

		yamlDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(yamlDir, "kevin.yaml"),
			[]byte("project: from-yaml\nplugins:\n  echo:\n    cmd: echo\nproxy:\n  listen: \"127.0.0.1:18080\"\n  gateway_port: 18081\nconsole:\n  listen: \"127.0.0.1:18082\"\n"), 0o600))
		yf, err := config.Load(yamlDir, "", nil)
		require.NoError(t, err)
		yamlSpecs, err := yf.Plugins()
		require.NoError(t, err)
		assert.Equal(t, cueSpecs, yamlSpecs)

		jsonDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(jsonDir, "kevin.json"),
			[]byte(`{"project":"from-yaml","plugins":{"echo":{"cmd":"echo"}},"proxy":{"listen":"127.0.0.1:18080","gateway_port":18081},"console":{"listen":"127.0.0.1:18082"}}`), 0o600))
		jf, err := config.Load(jsonDir, "", nil)
		require.NoError(t, err)
		jsonSpecs, err := jf.Plugins()
		require.NoError(t, err)
		assert.Equal(t, cueSpecs, jsonSpecs)
	})

	t.Run("reports ambiguous candidates", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(`plugins: {}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".kevin.cue"), []byte(`plugins: {}`), 0o600))

		_, err := config.Load(dir, "", nil)
		require.ErrorIs(t, err, config.ErrAmbiguous)
	})

	t.Run("rejects invalid content", func(t *testing.T) {
		tests := []struct {
			name         string
			src          string
			wantContains []string
		}{
			{
				name:         "broken cue",
				src:          `plugins: echo: cmd:`,
				wantContains: []string{"kevin.cue"},
			},
			{
				// project is a string in the core schema.
				name: "a conflict with the core schema",
				src:  `project: 42`,
			},
			{
				name: "a plugins key that breaks the reference grammar",
				src:  `plugins: "Not Valid!": cmd: "echo"`,
			},
			{
				name: "a checksum on a cmd source",
				src:  `plugins: mine: {cmd: "echo", checksum: "sha256:deadbeef"}`,
			},
			{
				name: "a checksum on an oci source",
				src:  `plugins: mine: {oci: "ghcr.io/acme/my-plugin:v1", checksum: "sha256:deadbeef"}`,
			},
			{
				name: "more than one source",
				src:  `plugins: mine: {cmd: "echo", oci: "ghcr.io/acme/my-plugin:v1"}`,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := config.Load(write(t, tt.src), "", nil)
				require.ErrorIs(t, err, config.ErrInvalid)
				for _, s := range tt.wantContains {
					assert.Contains(t, err.Error(), s)
				}
			})
		}
	})
}

func TestLoadPackageMode(t *testing.T) {
	t.Run("merges a package split across multiple files", func(t *testing.T) {
		dir := write(t, `package kevin

project: "base"
plugins: echo: cmd: "echo"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "extra.cue"), []byte(`package kevin

domain: "extra.test"
plugins: extra: cmd: "extra"`), 0o600))

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "base", cfg.Project)
		assert.Equal(t, "extra.test", cfg.Domain)
		assert.Len(t, cfg.Plugins, 2)
	})

	t.Run("excludes a sibling with a different package name", func(t *testing.T) {
		dir := write(t, `package kevin

project: "base"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "other.cue"), []byte(`package other

domain: "should-not-appear"`), 0o600))

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "base", cfg.Project)
		assert.Equal(t, "kevin.home", cfg.Domain, "other.cue's field must never merge in")
	})

	t.Run("excludes a package-less sibling silently", func(t *testing.T) {
		dir := write(t, `package kevin

project: "base"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.cue"), []byte(`domain: "should-not-appear"`), 0o600))

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "base", cfg.Project)
		assert.Equal(t, "kevin.home", cfg.Domain, "stray.cue's field must never merge in")
	})

	t.Run("rejects a package-mode sibling when the required file has no package clause", func(t *testing.T) {
		dir := write(t, `project: "base"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "extra.cue"), []byte(`package kevin

domain: "extra.test"`), 0o600))

		_, err := config.Load(dir, "", nil)
		require.ErrorIs(t, err, config.ErrPackageConflict)
	})

	t.Run("rejects a package-mode sibling when the required file is yaml", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.yaml"), []byte("project: base\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "extra.cue"), []byte(`package kevin

domain: "extra.test"`), 0o600))

		_, err := config.Load(dir, "", nil)
		require.ErrorIs(t, err, config.ErrPackageConflict)
	})

	t.Run("injects a tag value into a package-mode file", func(t *testing.T) {
		dir := write(t, `package kevin

project: "base"
airgap: bool | *false @tag(airgap,type=bool)
domain: *"default.test" | string
if airgap {
	domain: "airgap.test"
}`)
		f, err := config.Load(dir, "", []string{"airgap=true"})
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "airgap.test", cfg.Domain)
	})

	t.Run("a bare tag name is shorthand for name=true", func(t *testing.T) {
		dir := write(t, `package kevin

project: "base"
airgap: bool | *false @tag(airgap,type=bool)
domain: *"default.test" | string
if airgap {
	domain: "airgap.test"
}`)
		bare, err := config.Load(dir, "", []string{"airgap"})
		require.NoError(t, err)
		bareCfg, err := bare.Config()
		require.NoError(t, err)

		explicit, err := config.Load(dir, "", []string{"airgap=true"})
		require.NoError(t, err)
		explicitCfg, err := explicit.Config()
		require.NoError(t, err)

		assert.Equal(t, explicitCfg.Domain, bareCfg.Domain)
	})

	t.Run("rejects a tag with no matching @tag field", func(t *testing.T) {
		dir := write(t, `package kevin

project: "base"`)
		_, err := config.Load(dir, "", []string{"bogus=1"})
		require.Error(t, err)
	})

	t.Run("rejects a tag against a package-less file", func(t *testing.T) {
		dir := write(t, `project: "base"`)
		_, err := config.Load(dir, "", []string{"airgap=true"})
		require.ErrorIs(t, err, config.ErrTagWithoutPackage)
	})

	t.Run("a named environment's package-mode file never merges with a sibling's", func(t *testing.T) {
		dir := write(t, `package kevin

project: "base"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.cue"), []byte(`package kevin
`+listenBlockDefault+`
project: "staging-env"`), 0o600))

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "base", cfg.Project)

		f, err = config.Load(dir, "staging", nil)
		require.NoError(t, err)
		cfg, err = f.Config()
		require.NoError(t, err)
		assert.Equal(t, "staging-env", cfg.Project)
	})

	t.Run("a package-mode named sibling never conflicts with an unrelated package-less file", func(t *testing.T) {
		dir := write(t, `project: "base"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.cue"), []byte(`package kevin

project: "staging-env"`), 0o600))

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "base", cfg.Project)
	})
}

func TestPlugins(t *testing.T) {
	t.Run("decodes multiple plugins", func(t *testing.T) {
		f := load(t, `
plugins: {
	echo: cmd: "./bin/echo"
	kind: {
		cmd: "kevin-plugin-kind"
		args: ["--verbose"]
		env: LOG: "debug"
	}
}
`)
		plugins, err := f.Plugins()
		require.NoError(t, err)

		require.Len(t, plugins, 2)
		assert.Equal(t, "./bin/echo", plugins["echo"].Cmd)
		assert.Equal(t, []string{"--verbose"}, plugins["kind"].Args)
		assert.Equal(t, map[string]string{"LOG": "debug"}, plugins["kind"].Env)
	})

	t.Run("reports an incomplete value", func(t *testing.T) {
		// cmd has no default and no value here, thus the environment is not
		// concrete and cannot decode.
		f := load(t, `plugins: echo: cmd: string`)
		_, err := f.Plugins()
		require.ErrorIs(t, err, config.ErrInvalid)
	})

	t.Run("decodes each source kind", func(t *testing.T) {
		tests := []struct {
			name  string
			src   string
			check func(t *testing.T, spec config.PluginSpec)
		}{
			{
				name: "a file source",
				src:  `plugins: mine: file: "./plugin.tar"`,
				check: func(t *testing.T, spec config.PluginSpec) {
					t.Helper()
					assert.Equal(t, "./plugin.tar", spec.File)
				},
			},
			{
				name: "a file source with checksum and config",
				src: `plugins: mine: {
					file:     "./plugin.tar"
					checksum: "sha256:` + "deadbeef" + `"
					config: greeting: "hi"
				}`,
				check: func(t *testing.T, spec config.PluginSpec) {
					t.Helper()
					assert.Equal(t, "./plugin.tar", spec.File)
					assert.Equal(t, "sha256:deadbeef", spec.Checksum)
					assert.JSONEq(t, `{"greeting":"hi"}`, string(spec.Config))
				},
			},
			{
				name: "an oci source",
				src:  `plugins: mine: oci: "ghcr.io/acme/my-plugin:v1"`,
				check: func(t *testing.T, spec config.PluginSpec) {
					t.Helper()
					assert.Equal(t, "ghcr.io/acme/my-plugin:v1", spec.OCI)
				},
			},
			{
				name: "an oci source with args env and config",
				src: `plugins: mine: {
					oci: "ghcr.io/acme/my-plugin@sha256:deadbeef"
					args: ["--flag"]
					env: FOO: "bar"
					config: greeting: "hi"
				}`,
				check: func(t *testing.T, spec config.PluginSpec) {
					t.Helper()
					assert.Equal(t, "ghcr.io/acme/my-plugin@sha256:deadbeef", spec.OCI)
					assert.Equal(t, []string{"--flag"}, spec.Args)
					assert.Equal(t, map[string]string{"FOO": "bar"}, spec.Env)
					assert.JSONEq(t, `{"greeting":"hi"}`, string(spec.Config))
				},
			},
			{
				name: "an http source",
				src:  `plugins: mine: http: "https://example.com/my-plugin.tar.gz"`,
				check: func(t *testing.T, spec config.PluginSpec) {
					t.Helper()
					assert.Equal(t, "https://example.com/my-plugin.tar.gz", spec.HTTP)
				},
			},
			{
				name: "an http source with checksum and config",
				src: `plugins: mine: {
					http:     "https://example.com/my-plugin.tar.gz"
					checksum: "sha256:` + "deadbeef" + `"
					config: greeting: "hi"
				}`,
				check: func(t *testing.T, spec config.PluginSpec) {
					t.Helper()
					assert.Equal(t, "https://example.com/my-plugin.tar.gz", spec.HTTP)
					assert.Equal(t, "sha256:deadbeef", spec.Checksum)
					assert.JSONEq(t, `{"greeting":"hi"}`, string(spec.Config))
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				f := load(t, tt.src)
				specs, err := f.Plugins()
				require.NoError(t, err)
				tt.check(t, specs["mine"])
			})
		}
	})
}

func TestStepPlugins(t *testing.T) {
	t.Run("names the distinct plugins that setup and env steps use", func(t *testing.T) {
		f := load(t, `
plugins: {
	echo: cmd: "echo"
	kind: cmd: "kevin-plugin-kind"
}
setup: trust: uses: "kind:cluster"
env: {
	a: uses: "echo:echo"
	b: uses: "echo:widget"
}
`)
		names, err := f.StepPlugins()
		require.NoError(t, err)
		assert.Equal(t, []string{"echo", "kind"}, names, "the result is deduped and sorted")
	})

	t.Run("reports a bad step reference", func(t *testing.T) {
		f := load(t, `env: a: uses: "Not Valid"`)
		_, err := f.StepPlugins()
		require.ErrorIs(t, err, config.ErrBadStepRef)
	})

	t.Run("reports an incomplete value", func(t *testing.T) {
		// cmd has no default and no value here, thus the environment is not
		// concrete and cannot decode.
		f := load(t, `plugins: echo: cmd: string`)
		_, err := f.StepPlugins()
		require.ErrorIs(t, err, config.ErrInvalid)
	})
}

func TestValidateStepReferences(t *testing.T) {
	t.Run("accepts a builtin step with no plugins entry", func(t *testing.T) {
		f := load(t, `env: a: uses: "builtin:container"`)
		require.NoError(t, f.Validate(offers("builtin", "container", "kind", "trust")))
	})

	tests := []struct {
		name         string
		src          string
		schemas      map[string]config.PluginSchemas
		wantErr      error
		wantContains []string
	}{
		{
			name:    "a bad step reference",
			src:     `env: a: uses: "Not Valid"`,
			wantErr: config.ErrBadStepRef,
		},
		{
			name: "an undeclared plugin",
			src: `
plugins: echo: cmd: "echo"
env: a: uses: "missing:widget"
`,
			wantErr:      config.ErrUnknownPlugin,
			wantContains: []string{"env.a"},
		},
		{
			name: "an undeclared plugin in the setup scope",
			src: `
plugins: echo: cmd: "echo"
setup: trust: uses: "missing:widget"
`,
			wantErr: config.ErrUnknownPlugin,
		},
		{
			name: "an unknown plugin",
			src: `
plugins: acme: cmd: "echo"
env: a: uses: "nope:widget"
`,
			schemas:      offers("acme", "widget"),
			wantErr:      config.ErrUnknownPlugin,
			wantContains: []string{"acme", "builtin"},
		},
		{
			name:         "an unknown step type under builtin",
			src:          `env: a: uses: "builtin:nope"`,
			schemas:      offers("builtin", "container", "kind", "trust"),
			wantErr:      config.ErrUnknownStepType,
			wantContains: []string{"container"},
		},
		{
			name: "an unknown step type",
			src: `
plugins: echo: cmd: "echo"
env: a: uses: "echo:nope"
`,
			schemas:      offers("echo", "echo", "fail"),
			wantErr:      config.ErrUnknownStepType,
			wantContains: []string{"echo", "fail"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := load(t, tt.src)
			err := f.Validate(tt.schemas)
			require.ErrorIs(t, err, tt.wantErr)
			for _, s := range tt.wantContains {
				assert.Contains(t, err.Error(), s, "the error must name what the check found")
			}
		})
	}
}

func TestValidatePluginKeys(t *testing.T) {
	f := load(t, `plugins: "kevin": cmd: "echo"`)
	err := f.Validate(nil)
	require.ErrorIs(t, err, config.ErrReservedNamespace)
	assert.Contains(t, err.Error(), "builtin", "the error must name the reserved names")
	assert.Contains(t, err.Error(), "kevin")
}

func TestValidateWithBlock(t *testing.T) {
	t.Run("against the plugin's published schema", func(t *testing.T) {
		schemas := map[string]config.PluginSchemas{
			"echo": {Steps: map[string][]byte{"echo": []byte(`#Config: {
	image!: string
	replicas?: int
}`)}},
		}

		tests := []struct {
			name         string
			with         string
			expect       error
			wantContains []string
		}{
			{name: "a valid with block", with: `with: image: "nginx"`},
			{name: "every field present", with: `with: {image: "nginx", replicas: 3}`},
			{
				name: "an unknown field", with: `with: nonsense: true`, expect: config.ErrInvalid,
				wantContains: []string{"env.a.with", "kevin.cue"},
			},
			{name: "the wrong type", with: `with: image: 7`, expect: config.ErrInvalid},
			{name: "a missing required field", with: `with: replicas: 3`, expect: config.ErrInvalid},
			{name: "no with block at all", with: ``, expect: config.ErrInvalid},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				f := load(t, "plugins: echo: cmd: \"echo\"\nenv: a: {uses: \"echo:echo\"\n"+tt.with+"\n}")
				err := f.Validate(schemas)
				if tt.expect == nil {
					require.NoError(t, err)
					return
				}
				require.ErrorIs(t, err, tt.expect)
				assert.Contains(t, err.Error(), "env.a.with", "the error must name the step")
				for _, s := range tt.wantContains {
					assert.Contains(t, err.Error(), s)
				}
			})
		}
	})

	t.Run("accepts any config when the plugin publishes no schema", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: {uses: "echo:echo", with: anything: [1, 2, 3]}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
	})

	t.Run("a step name with a hyphen", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: "mirror-postgres": {uses: "echo:echo", with: anything: [1, 2, 3]}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
	})

	t.Run("rejects a broken plugin schema", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
`)
		err := f.Validate(map[string]config.PluginSchemas{
			"echo": {Steps: map[string][]byte{"echo": []byte(`#Config: {`)}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "echo:echo schema")
	})

	t.Run("rejects a schema without #Config", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
`)
		err := f.Validate(map[string]config.PluginSchemas{
			"echo": {Steps: map[string][]byte{"echo": []byte(`#Other: {}`)}},
		})
		require.ErrorIs(t, err, config.ErrInvalid)
		assert.Contains(t, err.Error(), "#Config")
	})
}

func TestValidateNeedsReferences(t *testing.T) {
	t.Run("a needs reference declared in needs", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	a: uses: "echo:echo"
	b: {uses: "echo:echo", needs: ["a"], with: msg: "${needs.a.out.x}"}
}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
	})

	t.Run("a needs reference not in needs", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	a: uses: "echo:echo"
	b: {uses: "echo:echo", with: msg: "${needs.a.out.x}"}
}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUndeclaredNeed)
		assert.Contains(t, err.Error(), "env.b")
		assert.Contains(t, err.Error(), "needs.a")
	})

	t.Run("a setup reference declared as setup.<name> in needs", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
setup: cluster: uses: "echo:echo"
env: app: {uses: "echo:echo", needs: ["setup.cluster"], with: msg: "${setup.cluster.out.x}"}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
	})

	t.Run("a setup reference with no matching needs entry", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
setup: cluster: uses: "echo:echo"
env: app: {uses: "echo:echo", with: msg: "${setup.cluster.out.x}"}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUndeclaredNeed)
		assert.Contains(t, err.Error(), "env.app")
		assert.Contains(t, err.Error(), "setup.cluster")
	})

	t.Run("a plain needs entry does not satisfy a setup reference", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
setup: cluster: uses: "echo:echo"
env: app: {uses: "echo:echo", needs: ["cluster"], with: msg: "${setup.cluster.out.x}"}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUndeclaredNeed)
	})

	t.Run("no with block at all is fine", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
	})

	t.Run("caught even when the plugin publishes no with schema", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	a: uses: "echo:echo"
	b: {uses: "echo:echo", with: msg: "${needs.a.out.x}"}
}
`)
		err := f.Validate(map[string]config.PluginSchemas{"echo": {Steps: map[string][]byte{"echo": nil}}})
		require.ErrorIs(t, err, config.ErrUndeclaredNeed)
	})

	t.Run("a malformed expression still surfaces an error", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: {uses: "echo:echo", with: msg: "${needs..a}"}
`)
		err := f.Validate(offers("echo", "echo"))
		require.Error(t, err)
	})
}

func TestValidateCommands(t *testing.T) {
	t.Run("a needs entry naming an exportable env step", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
commands: shell: {needs: ["a"], run: ["echo", "hi"]}
`)
		require.NoError(t, f.Validate(offersExport(true)))
	})

	t.Run("a needs entry naming an exportable setup step", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
setup: cluster: uses: "echo:echo"
commands: shell: {needs: ["setup.cluster"], run: ["echo", "hi"]}
`)
		require.NoError(t, f.Validate(offersExport(true)))
	})

	t.Run("a needs entry naming no such step", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
commands: shell: {needs: ["nope"], run: ["echo", "hi"]}
`)
		err := f.Validate(offersExport(true))
		require.ErrorIs(t, err, config.ErrUnknownStep)
		assert.Contains(t, err.Error(), "commands.shell")
		assert.Contains(t, err.Error(), `"nope"`)
	})

	t.Run("a needs entry naming no such setup step", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
commands: shell: {needs: ["setup.nope"], run: ["echo", "hi"]}
`)
		err := f.Validate(offersExport(true))
		require.ErrorIs(t, err, config.ErrUnknownStep)
	})

	t.Run("a needs entry naming a step whose plugin does not implement export", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
commands: shell: {needs: ["a"], run: ["echo", "hi"]}
`)
		err := f.Validate(offersExport(false))
		require.ErrorIs(t, err, config.ErrExportNotSupported)
		assert.Contains(t, err.Error(), "commands.shell")
		assert.Contains(t, err.Error(), `"a"`)
	})

	t.Run("no commands block at all is fine", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
	})

	t.Run("a run reference declared in needs", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
commands: shell: {needs: ["a"], run: ["echo", "${needs.a.out.x}"]}
`)
		require.NoError(t, f.Validate(offersExport(true)))
	})

	t.Run("a run reference not in needs", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
commands: shell: {run: ["echo", "${needs.a.out.x}"]}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUndeclaredNeed)
		assert.Contains(t, err.Error(), "commands.shell.run")
		assert.Contains(t, err.Error(), "needs.a")
	})

	t.Run("a run setup reference with no matching needs entry", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
setup: cluster: uses: "echo:echo"
commands: shell: {run: ["echo", "${setup.cluster.out.x}"]}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUndeclaredNeed)
	})
}

func TestValidatePluginConfig(t *testing.T) {
	tests := []struct {
		name         string
		src          string
		plugin       string
		configSchema []byte
		wantErr      error
		wantContains []string
	}{
		{
			name: "matches its schema",
			src: `
plugins: echo: {
	cmd:    "echo"
	config: greeting: "hi"
}
`,
			configSchema: []byte(`#Config: greeting!: string`),
		},
		{
			name: "the schema refuses it",
			src: `
plugins: echo: {
	cmd:    "echo"
	config: greeting: 7
}
`,
			configSchema: []byte(`#Config: greeting!: string`),
			wantErr:      config.ErrInvalid,
			wantContains: []string{"plugins.echo.config"},
		},
		{
			name: "no config schema is published",
			src: `
plugins: echo: {
	cmd:    "echo"
	config: greeting: "hi"
}
`,
			wantErr:      config.ErrConfigNotSupported,
			wantContains: []string{"echo"},
		},
		{
			name: "the published config schema is broken",
			src: `
plugins: echo: {
	cmd:    "echo"
	config: greeting: "hi"
}
`,
			configSchema: []byte(`#Config: {`),
			wantErr:      config.ErrInvalid,
			wantContains: []string{"echo schema"},
		},
		{
			name: "a plugin name with a hyphen",
			src: `
plugins: "echo-two": {
	cmd:    "echo"
	config: greeting: "hi"
}
`,
			plugin:       "echo-two",
			configSchema: []byte(`#Config: greeting!: string`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := load(t, tt.src)
			name := tt.plugin
			if name == "" {
				name = "echo"
			}
			schemas := offers(name, "echo")
			if tt.configSchema != nil {
				schemas[name] = config.PluginSchemas{Config: tt.configSchema, Steps: schemas[name].Steps}
			}

			err := f.Validate(schemas)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			for _, s := range tt.wantContains {
				assert.Contains(t, err.Error(), s)
			}
		})
	}
}

func TestConfig(t *testing.T) {
	t.Run("fills the defaults", func(t *testing.T) {
		f := load(t, `
project: "demo"
plugins: echo: cmd: "echo"
env: a: uses: "echo:echo"
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))

		cfg, err := f.Config()
		require.NoError(t, err)

		assert.Equal(t, "demo", cfg.Project)
		assert.Empty(t, cfg.Proxy.Egress.Allow)
		assert.Empty(t, cfg.Relay.Image)
	})

	t.Run("decodes the relay block", func(t *testing.T) {
		f := load(t, `
project: "demo"
plugins: echo: cmd: "echo"
relay: image: "kevin-relay:custom"
`)
		require.NoError(t, f.Validate(nil))

		cfg, err := f.Config()
		require.NoError(t, err)

		assert.Equal(t, "kevin-relay:custom", cfg.Relay.Image)
	})

	t.Run("names the project after the directory", func(t *testing.T) {
		tests := []struct {
			dirName string
			want    string
		}{
			{dirName: "My_Service 01!", want: "my-service-01"},
			{dirName: "!!!", want: "kevin"},
		}
		for _, tt := range tests {
			t.Run(tt.dirName, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), tt.dirName)
				require.NoError(t, os.Mkdir(dir, 0o750))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(listenBlockDefault+`plugins: {}`), 0o600))

				f, err := config.Load(dir, "", nil)
				require.NoError(t, err)
				cfg, err := f.Config()
				require.NoError(t, err)

				assert.Equal(t, tt.want, cfg.Project, "the name must be safe for a docker resource")
				assert.Empty(t, cfg.Name)
			})
		}
	})

	t.Run("folds a given name into the default project", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "My_Service")
		require.NoError(t, os.Mkdir(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Staging!.kevin.cue"), []byte(listenBlockDefault+`plugins: {}`), 0o600))

		f, err := config.Load(dir, "Staging!", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)

		assert.Equal(t, "staging", cfg.Name)
		assert.Equal(t, "my-service-staging", cfg.Project)
	})

	t.Run("an explicit project field wins over the default even with a name", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.cue"), []byte(listenBlockDefault+`project: "explicit"`), 0o600))

		f, err := config.Load(dir, "staging", nil)
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)

		assert.Equal(t, "explicit", cfg.Project)
		assert.Equal(t, "staging", cfg.Name, "Name still reports the resolved environment")
	})

	t.Run("rejects an incomplete value", func(t *testing.T) {
		// cmd has no default and no value here, thus the environment is not
		// concrete.
		f := load(t, `
plugins: echo: cmd: string
env: a: uses: "echo:echo"
`)
		_, err := f.Config()
		require.ErrorIs(t, err, config.ErrInvalid)
	})

	t.Run("rejects a missing proxy.listen/gateway_port/console.listen", func(t *testing.T) {
		// No proxy:/console: block at all - unlike load()'s fixtures, this
		// one deliberately skips listenBlockDefault to prove the three
		// fields carry no schema default of their own.
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(`project: "demo"`), 0o600))

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		_, err = f.Config()
		require.ErrorIs(t, err, config.ErrInvalid)
		assert.Contains(t, err.Error(), "proxy.listen")
	})

	t.Run("rejects a literal zero port", func(t *testing.T) {
		tests := []struct {
			name string
			src  string
			want string
		}{
			{name: "proxy.listen", src: `proxy: {listen: "127.0.0.1:0", gateway_port: 18081}
console: listen: "127.0.0.1:18082"`, want: "proxy.listen"},
			{name: "proxy.gateway_port", src: `proxy: {listen: "127.0.0.1:18080", gateway_port: 0}
console: listen: "127.0.0.1:18082"`, want: "proxy.gateway_port"},
			{name: "console.listen", src: `proxy: {listen: "127.0.0.1:18080", gateway_port: 18081}
console: listen: "127.0.0.1:0"`, want: "console.listen"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(tt.src+"\nproject: \"demo\"\n"), 0o600))

				_, err := config.Load(dir, "", nil)
				require.ErrorIs(t, err, config.ErrInvalid)
				assert.Contains(t, err.Error(), tt.want, "a literal 0 port is the muscle-memory mistake the regex/range constraint exists to catch")
			})
		}
	})
}

func TestSteps(t *testing.T) {
	t.Run("decodes needs and with per scope", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
setup: trust: uses: "echo:echo"
env: {
	a: uses: "echo:echo"
	b: {
		uses: "echo:echo"
		needs: ["a"]
		with: image: "nginx"
	}
}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
		cfg, err := f.Config()
		require.NoError(t, err)

		assert.Len(t, cfg.Steps(config.ScopeSetup), 1)

		env := cfg.Steps(config.ScopeEnv)
		require.Len(t, env, 2)
		assert.Equal(t, []string{"a"}, env["b"].Needs)
		assert.JSONEq(t, `{"image":"nginx"}`, string(env["b"].With))
		assert.Empty(t, env["a"].With, "a step without a with block carries no config")
	})

	t.Run("reads label", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	a: uses: "echo:echo"
	b: {
		uses:  "echo:echo"
		label: "Public API"
	}
}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
		cfg, err := f.Config()
		require.NoError(t, err)

		env := cfg.Steps(config.ScopeEnv)
		assert.Empty(t, env["a"].Label, "a step without a label carries none")
		assert.Equal(t, "Public API", env["b"].Label)
	})
}

// write puts src into a new kevin.cue, prefixed with listenBlockDefault so
// proxy.listen/proxy.gateway_port are always concrete, and returns the
// project directory. A src that opens with its own "package" clause gets
// the block inserted just after that line instead, since a package clause
// must be the first thing in the file.
func write(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	content := listenBlockDefault + src
	if line, rest, ok := strings.Cut(src, "\n"); ok && strings.HasPrefix(strings.TrimSpace(line), "package ") {
		content = line + "\n" + listenBlockDefault + rest
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(content), 0o600))
	return dir
}

// load reads a project and requires that the read succeeds.
func load(t *testing.T, src string) *config.File {
	t.Helper()
	f, err := config.Load(write(t, src), "", nil)
	require.NoError(t, err)
	return f
}

// offers builds a schemas map with one plugin that offers the given step
// types. Each step type publishes no with-block schema.
func offers(plugin string, steps ...string) map[string]config.PluginSchemas {
	m := make(map[string][]byte, len(steps))
	for _, s := range steps {
		m[s] = nil
	}
	return map[string]config.PluginSchemas{plugin: {Steps: m}}
}

// offersExport builds a schemas map with one "echo" plugin offering the
// "echo" step type, reporting export as whether that step type implements
// Export.
func offersExport(export bool) map[string]config.PluginSchemas {
	return map[string]config.PluginSchemas{"echo": {
		Steps:  map[string][]byte{"echo": nil},
		Export: map[string]bool{"echo": export},
	}}
}
