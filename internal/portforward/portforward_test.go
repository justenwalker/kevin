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
		{name: "plain host:port is not ok", address: "127.0.0.1:5432", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay, target, ok := SplitSOCKS5(tc.address)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.relay, relay)
				assert.Equal(t, tc.target, target)
			}
		})
	}
}
