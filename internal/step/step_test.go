package step

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUpstreamDial(t *testing.T) {
	t.Run("an address dials as itself", func(t *testing.T) {
		assert.Equal(t, "127.0.0.1:8080", Upstream{Addr: "127.0.0.1:8080"}.Dial())
	})

	t.Run("a relay target dials through socks5", func(t *testing.T) {
		u := Upstream{Relay: &RelayTarget{Relay: "127.0.0.1:1080", Target: "db:5432"}}

		assert.Equal(t, "socks5://127.0.0.1:1080/db:5432", u.Dial())
	})
}

func TestKindString(t *testing.T) {
	assert.Equal(t, "resource", KindResource.String())
	assert.Equal(t, "action", KindAction.String())
	assert.Equal(t, "probe", KindProbe.String())
	assert.Empty(t, KindUnspecified.String())
}

func TestProtocolString(t *testing.T) {
	assert.Equal(t, "tcp", TCP.String())
	assert.Equal(t, "udp", UDP.String())
}

func TestEngineNamed(t *testing.T) {
	assert.Equal(t, EnginePodman, EngineNamed("podman"))
	assert.Equal(t, EngineDocker, EngineNamed("docker"))
	assert.Equal(t, EngineDocker, EngineNamed(""))
}
