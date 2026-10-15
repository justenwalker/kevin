package plugin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetail(t *testing.T) {
	tests := []struct {
		name string
		give interface{ Detail() Detail }
		want Detail
	}{
		{
			name: "Route is a copyable link to the host",
			give: Route{Host: "api.test", Upstream: Upstream{Addr: "api:8080"}, TLS: true},
			want: Detail{Value: String("api.test"), Href: "https://api.test", Copyable: true},
		},
		{
			name: "an intercept route is plain text - it's not a single address to browse to",
			give: Route{Host: "s3.amazonaws.com", Upstream: Upstream{Addr: "127.0.0.1:9000"}, Intercept: &RouteIntercept{Ports: []int{443}}},
			want: Detail{Value: String("s3.amazonaws.com"), Copyable: true},
		},
		{
			name: "a wildcard host is plain text for the same reason",
			give: Route{Host: "*.myapp.kevin.test", Upstream: Upstream{Addr: "myapp:8080"}},
			want: Detail{Value: String("*.myapp.kevin.test"), Copyable: true},
		},
		{
			name: "ExposedPort labels protocol and name",
			give: ExposedPort{Name: "postgres", Upstream: Upstream{Addr: "127.0.0.1:54321"}},
			want: Detail{Label: "tcp postgres", Value: String("127.0.0.1:54321"), Copyable: true},
		},
		{
			name: "a relay-routed ExposedPort marks the label so a relay hop stays visible",
			give: ExposedPort{Name: "postgres", Upstream: Upstream{Relay: &RelayTarget{Relay: "127.0.0.1:1080", Target: "postgres:5432"}}},
			want: Detail{Label: "tcp postgres (relay)", Value: String("socks5://127.0.0.1:1080/postgres:5432"), Copyable: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.give.Detail())
		})
	}
}

func TestProtocolUnmarshalText(t *testing.T) {
	tests := []struct {
		text string
		want Protocol
	}{
		{"", ProtocolTCP},
		{"tcp", ProtocolTCP},
		{"udp", ProtocolUDP},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			var p Protocol
			require.NoError(t, p.UnmarshalText([]byte(tt.text)))
			assert.Equal(t, tt.want, p)
		})
	}

	t.Run("an unknown protocol is an error", func(t *testing.T) {
		var p Protocol
		require.ErrorIs(t, p.UnmarshalText([]byte("sctp")), ErrUnknownProtocol)
	})

	t.Run("a with-block field decodes from JSON", func(t *testing.T) {
		var got struct {
			Protocol Protocol `json:"protocol"`
		}
		require.NoError(t, json.Unmarshal([]byte(`{"protocol":"udp"}`), &got))
		assert.Equal(t, ProtocolUDP, got.Protocol)
	})
}

func TestEngineAndScopeStrings(t *testing.T) {
	assert.Equal(t, "docker", EngineDocker.String())
	assert.Equal(t, "podman", EnginePodman.String())
	assert.Equal(t, "env", ScopeEnv.String())
	assert.Equal(t, "setup", ScopeSetup.String())
	assert.Empty(t, ScopeUnspecified.String())
}
