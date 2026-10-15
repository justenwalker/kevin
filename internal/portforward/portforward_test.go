package portforward

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitSOCKS5(t *testing.T) {
	cases := []struct {
		name, address, relay, target string
		ok                           bool
	}{
		{name: "socks5 url splits relay and target", address: "socks5://127.0.0.1:1080/postgres:5432", relay: "127.0.0.1:1080", target: "postgres:5432", ok: true},
		{name: "a socks5 url with no target is not ok", address: "socks5://127.0.0.1:1080", ok: false},
		{name: "plain host:port is not ok", address: "127.0.0.1:5432", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SplitSOCKS5(tc.address)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, RelayTarget{Relay: tc.relay, Target: tc.target}, got)
			}
		})
	}
}
