package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
)

func TestUDPPoolSize(t *testing.T) {
	t.Run("defaults to defaultUDPPoolSize when unset", func(t *testing.T) {
		t.Setenv(UDPPoolSizeEnvVar, "")
		n, err := UDPPoolSize()
		require.NoError(t, err)
		assert.Equal(t, defaultUDPPoolSize, n)
	})

	t.Run("reads a configured size", func(t *testing.T) {
		t.Setenv(UDPPoolSizeEnvVar, "4")
		n, err := UDPPoolSize()
		require.NoError(t, err)
		assert.Equal(t, 4, n)
	})

	t.Run("zero disables the pool", func(t *testing.T) {
		t.Setenv(UDPPoolSizeEnvVar, "0")
		n, err := UDPPoolSize()
		require.NoError(t, err)
		assert.Equal(t, 0, n)
	})

	t.Run("rejects a negative size", func(t *testing.T) {
		t.Setenv(UDPPoolSizeEnvVar, "-1")
		_, err := UDPPoolSize()
		require.ErrorIs(t, err, ErrInvalidUDPPoolSize)
	})

	t.Run("rejects a non-integer value", func(t *testing.T) {
		t.Setenv(UDPPoolSizeEnvVar, "many")
		_, err := UDPPoolSize()
		require.ErrorIs(t, err, ErrInvalidUDPPoolSize)
	})
}

func TestUDPRelayPorts(t *testing.T) {
	assert.Equal(t, []int{40000, 40001, 40002}, udpRelayPorts(3))
	assert.Empty(t, udpRelayPorts(0))
}

func TestUDPRelayPortsArg(t *testing.T) {
	t.Run("appends the pool range when n is positive", func(t *testing.T) {
		assert.Equal(t, []string{"forward", "--udp-relay-ports", "40000-40002"}, udpRelayPortsArg(3, []string{"forward"}))
	})

	t.Run("leaves args untouched when n is zero", func(t *testing.T) {
		assert.Equal(t, []string{"forward"}, udpRelayPortsArg(0, []string{"forward"}))
	})
}

func TestUDPAddrsFromInfo(t *testing.T) {
	t.Run("extracts every udp port, trimming the /udp suffix", func(t *testing.T) {
		info := cri.Container{Ports: map[string]string{
			"1080/tcp":  "127.0.0.1:54321",
			"40000/udp": "127.0.0.1:41000",
			"40001/udp": "127.0.0.1:41001",
		}}
		assert.Equal(t, map[string]string{"40000": "127.0.0.1:41000", "40001": "127.0.0.1:41001"}, udpAddrsFromInfo(info))
	})

	t.Run("reports nil when the container publishes no udp port", func(t *testing.T) {
		info := cri.Container{Ports: map[string]string{"1080/tcp": "127.0.0.1:54321"}}
		assert.Nil(t, udpAddrsFromInfo(info))
	})
}
