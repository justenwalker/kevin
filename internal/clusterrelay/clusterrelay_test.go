package clusterrelay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/relay"
)

func TestAddr(t *testing.T) {
	assert.Equal(t, "127.0.0.1:54321", Addr(54321))
}

func TestPickHostPort(t *testing.T) {
	port, err := findFreePort(t.Context())
	require.NoError(t, err)
	assert.Positive(t, port)
}

func TestFindFreePorts(t *testing.T) {
	t.Run("returns n distinct ports", func(t *testing.T) {
		ports, err := findFreePorts(t.Context(), 4)
		require.NoError(t, err)
		require.Len(t, ports, 4)

		seen := make(map[int]bool, len(ports))
		for _, p := range ports {
			assert.Positive(t, p)
			assert.False(t, seen[p], "each port must be distinct")
			seen[p] = true
		}
	})

	t.Run("n zero returns no ports", func(t *testing.T) {
		ports, err := findFreePorts(t.Context(), 0)
		require.NoError(t, err)
		assert.Empty(t, ports)
	})
}

func TestUDPAddrs(t *testing.T) {
	t.Run("maps each fixed node port to its host address", func(t *testing.T) {
		got := UDPAddrs([]int{41000, 41001})
		assert.Equal(t, map[string]string{
			"40000": "127.0.0.1:41000",
			"40001": "127.0.0.1:41001",
		}, got)
	})

	t.Run("no host ports reports nil", func(t *testing.T) {
		assert.Nil(t, UDPAddrs(nil))
	})
}

func TestPodManifest(t *testing.T) {
	t.Run("no udp pool adds no udp args or ports", func(t *testing.T) {
		got := PodManifest("kind-example-control-plane", "kevin-relay:dev", 0)

		assert.Contains(t, got, "nodeName: kind-example-control-plane")
		assert.Contains(t, got, "image: kevin-relay:dev")
		assert.Contains(t, got, "imagePullPolicy: Never")
		assert.Contains(t, got, `args: ["socks5-gateway", "--listen", ":1080"]`)
		assert.Contains(t, got, "containerPort: 1080")
		assert.Contains(t, got, "hostPort: 1080")
		assert.NotContains(t, got, "udp-relay-ports")
		assert.NotContains(t, got, "UDP")
	})

	t.Run("a udp pool adds the flag and one ports entry per port", func(t *testing.T) {
		got := PodManifest("kind-example-control-plane", "kevin-relay:dev", 3)

		assert.Contains(t, got, `"--udp-relay-ports", "40000-40002"`)
		assert.Contains(t, got, "containerPort: 40000\n      hostPort: 40000\n      protocol: UDP")
		assert.Contains(t, got, "containerPort: 40001\n      hostPort: 40001\n      protocol: UDP")
		assert.Contains(t, got, "containerPort: 40002\n      hostPort: 40002\n      protocol: UDP")
	})
}

func TestPickPorts(t *testing.T) {
	t.Run("a disabled pool reserves the tcp port only", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "0")

		got, err := PickPorts(t.Context())
		require.NoError(t, err)
		assert.Positive(t, got.TCP)
		assert.Empty(t, got.UDP)
	})

	t.Run("reserves one udp port per pool entry", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "3")

		got, err := PickPorts(t.Context())
		require.NoError(t, err)
		assert.Positive(t, got.TCP)
		assert.Len(t, got.UDP, 3)
		assert.NotContains(t, got.UDP, got.TCP)
	})

	t.Run("an invalid pool size is an error", func(t *testing.T) {
		t.Setenv(relay.UDPPoolSizeEnvVar, "many")

		_, err := PickPorts(t.Context())
		require.ErrorIs(t, err, relay.ErrInvalidUDPPoolSize)
	})
}
