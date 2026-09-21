package engine

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pkgtrust"
	"github.com/justenwalker/kevin/internal/sigstorepkg"
	"github.com/justenwalker/kevin/internal/uerr"
)

func TestFriendlySignatureErr(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			name:    "signature missing",
			err:     pkgtrust.ErrSignatureMissing,
			wantMsg: "plugins.acme has a signing block but ships no signature file - remove signing, or add the signature",
		},
		{
			name:    "unknown key",
			err:     pkgtrust.ErrUnknownKeyID,
			wantMsg: "plugins.acme's signature key isn't trusted - run `kevin plugin trust add <keyfile>` first",
		},
		{
			name:    "signature invalid",
			err:     pkgtrust.ErrSignatureInvalid,
			wantMsg: "plugins.acme's signature doesn't verify against its package - it may be corrupted or tampered with",
		},
		{
			name:    "identity untrusted",
			err:     pkgtrust.ErrIdentityUntrusted,
			wantMsg: "plugins.acme's signing identity isn't trusted - run `kevin plugin trust add-identity` first",
		},
		{
			name:    "sigstore verify failed",
			err:     sigstorepkg.ErrVerifyFailed,
			wantMsg: "plugins.acme's sigstore signature doesn't verify against its package - it may be corrupted or tampered with",
		},
		{
			name:    "cosign not found",
			err:     sigstorepkg.ErrCosignNotFound,
			wantMsg: "plugins.acme needs cosign to verify its sigstore signature - install it: https://docs.sigstore.dev/cosign/system_config/installation/",
		},
		{
			name: "an unrelated failure is left alone",
			err:  errors.New("read the package: permission denied"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := friendlySignatureErr(tt.err, "acme")
			require.ErrorIs(t, got, tt.err)
			if tt.wantMsg == "" {
				assert.Equal(t, tt.err.Error(), uerr.Display(got))
				return
			}
			assert.Equal(t, tt.wantMsg, uerr.Display(got))
		})
	}
}
