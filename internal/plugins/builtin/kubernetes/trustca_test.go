package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

const testCAPEM = "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n"

func TestWantsTrustCA(t *testing.T) {
	tests := []struct {
		name    string
		trustCA bool
		caPEM   string
		want    bool
	}{
		{name: "trust_ca is on and the environment carries a certificate", trustCA: true, caPEM: "-----BEGIN CERTIFICATE-----", want: true},
		{name: "the environment carries no certificate", trustCA: true, caPEM: "", want: false},
		{name: "the step opts out", trustCA: false, caPEM: "-----BEGIN CERTIFICATE-----", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wantsTrustCA(config{TrustCA: tt.trustCA}, plugin.Env{CAPath: tt.caPEM})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizePEM(t *testing.T) {
	assert.Equal(t, "line one\nline two", normalizePEM("  line one\r\nline two  "))
}

func TestTrustCAFromPath(t *testing.T) {
	t.Run("hands the certificate at the given path to the driver", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(path, []byte(testCAPEM), 0o600))

		var gotNodes []string
		var gotPEM string
		drv := fakeDriver{trustCA: func(_ context.Context, nodes []string, caPEM string) error {
			gotNodes, gotPEM = nodes, caPEM
			return nil
		}}

		require.NoError(t, trustCAFromPath(t.Context(), drv, []string{"demo-control-plane"}, path, &capture{}))
		assert.Equal(t, []string{"demo-control-plane"}, gotNodes)
		assert.Equal(t, testCAPEM, gotPEM)
	})

	t.Run("a missing path is an error", func(t *testing.T) {
		err := trustCAFromPath(t.Context(), fakeDriver{}, []string{"demo-control-plane"}, filepath.Join(t.TempDir(), "missing.pem"), &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the kevin root certificate")
	})

	t.Run("the driver failing propagates", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(path, []byte(testCAPEM), 0o600))

		drv := fakeDriver{trustCA: func(context.Context, []string, string) error { return assert.AnError }}
		require.ErrorIs(t, trustCAFromPath(t.Context(), drv, []string{"demo-control-plane"}, path, &capture{}), assert.AnError)
	})
}
