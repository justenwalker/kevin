package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

func TestLoad(t *testing.T) {
	t.Run("reports a missing file", func(t *testing.T) {
		_, err := config.Load(t.TempDir(), "")
		require.ErrorIs(t, err, config.ErrNotFound)
	})

	t.Run("reports a read error that isn't a missing file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "kevin.cue"), 0o750), "kevin.cue as a directory forces a read error")

		_, err := config.Load(dir, "")
		require.Error(t, err)
		require.NotErrorIs(t, err, config.ErrNotFound)
	})

	t.Run("resolves a relative directory", func(t *testing.T) {
		dir := write(t, `project: "rel"`)
		f, err := config.Load(dir, "")
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(f.Dir()), "Dir must be absolute")
	})

	t.Run("resolves a named environment", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.cue"), []byte(`project: "staging-env"`), 0o600))

		f, err := config.Load(dir, "staging")
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "staging-env", cfg.Project)
	})

	t.Run("resolves a dotfile-named environment", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".staging.kevin.yaml"), []byte("project: staging-hidden\n"), 0o600))

		f, err := config.Load(dir, "staging")
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "staging-hidden", cfg.Project)
	})

	t.Run("resolves yaml and json the same as an equivalent cue file", func(t *testing.T) {
		cueDir := write(t, `project: "from-yaml"
plugins: echo: cmd: "echo"`)
		cf, err := config.Load(cueDir, "")
		require.NoError(t, err)
		cueSpecs, err := cf.Plugins()
		require.NoError(t, err)

		yamlDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(yamlDir, "kevin.yaml"), []byte("project: from-yaml\nplugins:\n  echo:\n    cmd: echo\n"), 0o600))
		yf, err := config.Load(yamlDir, "")
		require.NoError(t, err)
		yamlSpecs, err := yf.Plugins()
		require.NoError(t, err)
		assert.Equal(t, cueSpecs, yamlSpecs)

		jsonDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(jsonDir, "kevin.json"), []byte(`{"project":"from-yaml","plugins":{"echo":{"cmd":"echo"}}}`), 0o600))
		jf, err := config.Load(jsonDir, "")
		require.NoError(t, err)
		jsonSpecs, err := jf.Plugins()
		require.NoError(t, err)
		assert.Equal(t, cueSpecs, jsonSpecs)
	})

	t.Run("reports ambiguous candidates", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(`plugins: {}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".kevin.cue"), []byte(`plugins: {}`), 0o600))

		_, err := config.Load(dir, "")
		require.ErrorIs(t, err, config.ErrAmbiguous)
	})

	t.Run("merges an optional local override file", func(t *testing.T) {
		dir := write(t, `project: "base"
plugins: echo: cmd: "echo"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.local.cue"), []byte(`domain: "local.test"
plugins: extra: cmd: "extra"`), 0o600))

		f, err := config.Load(dir, "")
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "base", cfg.Project)
		assert.Equal(t, "local.test", cfg.Domain)
		assert.Len(t, cfg.Plugins, 2)
	})

	t.Run("ignores a missing local override file", func(t *testing.T) {
		dir := write(t, `project: "base"`)
		f, err := config.Load(dir, "")
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "base", cfg.Project)
	})

	t.Run("rejects a local override that conflicts with a concrete base value", func(t *testing.T) {
		dir := write(t, `project: "base"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.local.cue"), []byte(`project: "other"`), 0o600))

		_, err := config.Load(dir, "")
		require.ErrorIs(t, err, config.ErrInvalid)
	})

	t.Run("reports ambiguous local override candidates", func(t *testing.T) {
		dir := write(t, `project: "base"`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.local.cue"), []byte(`plugins: {}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".kevin.local.cue"), []byte(`plugins: {}`), 0o600))

		_, err := config.Load(dir, "")
		require.ErrorIs(t, err, config.ErrAmbiguous)
	})

	t.Run("resolves a named environment's local override file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.cue"), []byte(`project: "staging-env"`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.local.cue"), []byte(`domain: "local.test"`), 0o600))

		f, err := config.Load(dir, "staging")
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)
		assert.Equal(t, "local.test", cfg.Domain)
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
				_, err := config.Load(write(t, tt.src), "")
				require.ErrorIs(t, err, config.ErrInvalid)
				for _, s := range tt.wantContains {
					assert.Contains(t, err.Error(), s)
				}
			})
		}
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

func TestValidatePluginConfig(t *testing.T) {
	tests := []struct {
		name         string
		src          string
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := load(t, tt.src)
			schemas := offers("echo", "echo")
			if tt.configSchema != nil {
				schemas["echo"] = config.PluginSchemas{Config: tt.configSchema, Steps: schemas["echo"].Steps}
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
		assert.Equal(t, "127.0.0.1:0", cfg.Proxy.Listen)
		assert.Equal(t, "127.0.0.1:0", cfg.Console.Listen)
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
				require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(`plugins: {}`), 0o600))

				f, err := config.Load(dir, "")
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
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Staging!.kevin.cue"), []byte(`plugins: {}`), 0o600))

		f, err := config.Load(dir, "Staging!")
		require.NoError(t, err)
		cfg, err := f.Config()
		require.NoError(t, err)

		assert.Equal(t, "staging", cfg.Name)
		assert.Equal(t, "my-service-staging", cfg.Project)
	})

	t.Run("an explicit project field wins over the default even with a name", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staging.kevin.cue"), []byte(`project: "explicit"`), 0o600))

		f, err := config.Load(dir, "staging")
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

// write puts src into a new kevin.cue and returns the project directory.
func write(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(src), 0o600))
	return dir
}

// load reads a project and requires that the read succeeds.
func load(t *testing.T, src string) *config.File {
	t.Helper()
	f, err := config.Load(write(t, src), "")
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
