package pluginindex

import (
	"testing"

	"cuelang.org/go/cue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAgainstPluginMeta(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "valid, full",
			yaml: "name: demo\nsummary: a demo plugin\nhomepage: https://example.com\nmaintainer: someone\n",
		},
		{
			name: "valid, minimal",
			yaml: "name: demo\nsummary: a demo plugin\n",
		},
		{
			name:    "missing name",
			yaml:    "summary: a demo plugin\n",
			wantErr: true,
		},
		{
			name:    "missing summary",
			yaml:    "name: demo\n",
			wantErr: true,
		},
		{
			name:    "invalid name characters",
			yaml:    "name: Demo_Plugin\nsummary: a demo plugin\n",
			wantErr: true,
		},
		{
			name: "minisign signer",
			yaml: "name: demo\nsummary: a demo plugin\nsigners:\n  - scheme: minisign\n    key: |\n      untrusted comment: test\n      RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3\n",
		},
		{
			name: "sigstore signer",
			yaml: "name: demo\nsummary: a demo plugin\nsigners:\n  - scheme: sigstore\n    identity: ci@example.com\n    issuer: https://token.actions.githubusercontent.com\n",
		},
		{
			name: "both signer kinds",
			yaml: "name: demo\nsummary: a demo plugin\nsigners:\n  - scheme: minisign\n    key: |\n      untrusted comment: test\n      RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3\n  - scheme: sigstore\n    identity: ci@example.com\n    issuer: https://token.actions.githubusercontent.com\n",
		},
		{
			name:    "sigstore signer missing issuer",
			yaml:    "name: demo\nsummary: a demo plugin\nsigners:\n  - scheme: sigstore\n    identity: ci@example.com\n",
			wantErr: true,
		},
		{
			name:    "minisign signer missing key",
			yaml:    "name: demo\nsummary: a demo plugin\nsigners:\n  - scheme: minisign\n",
			wantErr: true,
		},
		{
			name:    "unknown signer scheme",
			yaml:    "name: demo\nsummary: a demo plugin\nsigners:\n  - scheme: pgp\n",
			wantErr: true,
		},
		{
			name: "version_source",
			yaml: "name: demo\nsummary: a demo plugin\nversion_source: https://example.com/demo-versions.git\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateAgainst(cue.ParsePath("#PluginMeta"), "plugin.yaml", []byte(tt.yaml))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateAgainstVersion(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "oci source, unsigned",
			yaml: "version: 1.0.0\nsource:\n  oci: ghcr.io/example/demo:v1.0.0\n",
		},
		{
			name: "oci source, minisign",
			yaml: "version: 1.0.0\nsource:\n  oci: ghcr.io/example/demo:v1.0.0\n  signing:\n    scheme: minisign\n",
		},
		{
			name: "file source, sigstore",
			yaml: "version: 1.0.0\nsource:\n  file: ./demo.tar\n  signing:\n    scheme: sigstore\n    identity: ci@example.com\n    issuer: https://token.actions.githubusercontent.com\n",
		},
		{
			name: "http source",
			yaml: "version: 1.0.0\nsource:\n  http: https://example.com/demo.tar\n",
		},
		{
			name:    "cmd source rejected",
			yaml:    "version: 1.0.0\nsource:\n  cmd: /usr/local/bin/demo\n",
			wantErr: true,
		},
		{
			name:    "missing version",
			yaml:    "source:\n  oci: ghcr.io/example/demo:v1.0.0\n",
			wantErr: true,
		},
		{
			name:    "missing source",
			yaml:    "version: 1.0.0\n",
			wantErr: true,
		},
		{
			name:    "malformed version string",
			yaml:    "version: v1\nsource:\n  oci: ghcr.io/example/demo:v1.0.0\n",
			wantErr: true,
		},
		{
			name: "prerelease version string",
			yaml: "version: 1.0.0-rc1\nsource:\n  oci: ghcr.io/example/demo:v1.0.0-rc1\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateAgainst(cue.ParsePath("#Version"), "versions/x.yaml", []byte(tt.yaml))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateAgainstIndex(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "layout 1",
			yaml: "layout: 1\n",
		},
		{
			name:    "unknown layout",
			yaml:    "layout: 2\n",
			wantErr: true,
		},
		{
			name:    "missing layout",
			yaml:    "{}\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateAgainst(cue.ParsePath("#Index"), "kevin-index.yaml", []byte(tt.yaml))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSchemaCompiles(t *testing.T) {
	assert.NoError(t, schema.Err())
}
