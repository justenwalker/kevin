package cmd

import (
	"bytes"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrintCheck(t *testing.T) {
	tests := []struct {
		name   string
		ok     bool
		skip   bool
		detail string
		want   string
	}{
		{name: "ok with no detail", ok: true, want: "docker: ok\n"},
		{name: "ok with detail", ok: true, detail: "3 profiles", want: "docker: ok (3 profiles)\n"},
		{name: "fail", detail: "daemon does not answer", want: "docker: fail (daemon does not answer)\n"},
		{name: "skip wins over ok", ok: true, skip: true, detail: "no Firefox profile", want: "docker: skip (no Firefox profile)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printCheck(&buf, "docker", tt.ok, tt.skip, tt.detail)
			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func TestLoadProjectConfig(t *testing.T) {
	t.Run("reports (nil, nil) when there is no environment file", func(t *testing.T) {
		var buf bytes.Buffer
		opts := &options{dir: t.TempDir()}

		cfg, err := loadProjectConfig(&buf, opts)

		require.NoError(t, err)
		assert.Nil(t, cfg)
		assert.Empty(t, buf.String(), "checkPorts reports its own skip line, not loadProjectConfig")
	})

	t.Run("fails on a load error other than a missing file", func(t *testing.T) {
		dir := t.TempDir()
		// Two candidate environment files in the same directory: config.Load
		// fails with ErrAmbiguous, not ErrNotFound.
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(`project: "x"`), 0o600))
		require.NoError(t, os.WriteFile(dir+"/kevin.yaml", []byte("project: x\n"), 0o600))

		var buf bytes.Buffer
		opts := &options{dir: dir}

		cfg, err := loadProjectConfig(&buf, opts)

		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, buf.String(), "config: fail")
	})

	t.Run("fails when the environment does not resolve to concrete values", func(t *testing.T) {
		dir := t.TempDir()
		// No proxy/console block: config.Load succeeds (the file parses and
		// unifies), but f.Config's concreteness check fails on the missing
		// required fields.
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(`project: "x"`), 0o600))

		var buf bytes.Buffer
		opts := &options{dir: dir}

		cfg, err := loadProjectConfig(&buf, opts)

		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, buf.String(), "config: fail")
	})
}

func TestCheckEngine(t *testing.T) {
	t.Run("probes the named engine", func(t *testing.T) {
		var buf bytes.Buffer
		checkEngine(t.Context(), &buf, "docker")
		assert.Contains(t, buf.String(), "docker: ")
	})

	t.Run("probes podman when that's the resolved engine", func(t *testing.T) {
		var buf bytes.Buffer
		checkEngine(t.Context(), &buf, "podman")
		assert.Contains(t, buf.String(), "podman: ")
	})

	t.Run("reports an unsupported engine", func(t *testing.T) {
		var buf bytes.Buffer
		ok := checkEngine(t.Context(), &buf, "bogus")
		assert.False(t, ok)
		assert.Contains(t, buf.String(), "bogus: fail")
	})
}

func TestCheckPorts(t *testing.T) {
	t.Run("skips when there is no project", func(t *testing.T) {
		var buf bytes.Buffer

		ok := checkPorts(t.Context(), &buf, nil)

		assert.True(t, ok, "a missing environment file must not fail doctor")
		assert.Contains(t, buf.String(), "ports: skip (no environment file in this directory)")
	})

	t.Run("reports a free port as ok and a bound one as fail", func(t *testing.T) {
		var lc net.ListenConfig

		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = ln.Close() })
		busy := ln.Addr().String()

		// A real port that was free a moment ago and stays free, since
		// nothing else on the machine grabs it in between.
		free, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		freeAddr := free.Addr().String()
		require.NoError(t, free.Close())

		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/kevin.cue", []byte(
			`proxy: {listen: "`+busy+`", gateway_port: 18081, egress: deny: true}
console: listen: "`+freeAddr+`"
project: "x"
`), 0o600))

		var loadBuf bytes.Buffer
		cfg, err := loadProjectConfig(&loadBuf, &options{dir: dir})
		require.NoError(t, err)

		var buf bytes.Buffer
		ok := checkPorts(t.Context(), &buf, cfg)

		assert.False(t, ok, "a bound proxy port must fail doctor")
		out := buf.String()
		assert.Contains(t, out, "port proxy ("+busy+"): fail (in use)")
		assert.Contains(t, out, "port console ("+freeAddr+"): ok")
		assert.Contains(t, out, "port gateway_port: skip")
	})
}
