package main

import (
	"fmt"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddOffset(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		offset uint64
		want   string
	}{
		{name: "ipv4 plus one", base: "198.18.0.0", offset: 1, want: "198.18.0.1"},
		{name: "ipv4 carries into the next byte", base: "198.18.0.0", offset: 256, want: "198.18.1.0"},
		{name: "ipv6 plus one", base: "100::", offset: 1, want: "100::1"},
		{name: "ipv6 large offset", base: "100::", offset: 0x1_0000, want: "100::1:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := netip.MustParseAddr(tt.base)
			got := addOffset(base, tt.offset)
			assert.Equal(t, netip.MustParseAddr(tt.want), got)
			assert.Equal(t, base.Is4(), got.Is4(), "family must survive the offset")
		})
	}
}

func TestFakeIPPoolAllocate(t *testing.T) {
	t.Run("allocates a stable address per host", func(t *testing.T) {
		p, err := newFakeIPPool("198.18.0.0/15", "100::/64")
		require.NoError(t, err)

		first, err := p.allocate("api.example.com")
		require.NoError(t, err)
		assert.NotEmpty(t, first.V4)
		assert.NotEmpty(t, first.V6)

		again, err := p.allocate("api.example.com")
		require.NoError(t, err)
		assert.Equal(t, first, again, "the same host must get the same address back, not a new one")
	})

	t.Run("different hosts get different addresses", func(t *testing.T) {
		p, err := newFakeIPPool("198.18.0.0/15", "100::/64")
		require.NoError(t, err)

		a, err := p.allocate("a.example.com")
		require.NoError(t, err)
		b, err := p.allocate("b.example.com")
		require.NoError(t, err)

		assert.NotEqual(t, a.V4, b.V4)
		assert.NotEqual(t, a.V6, b.V6)
	})

	t.Run("an exhausted pool errors instead of allocating outside the range", func(t *testing.T) {
		p, err := newFakeIPPool("10.0.0.0/30", "100::/126")
		require.NoError(t, err)

		var lastErr error
		for i := range 5 {
			_, lastErr = p.allocate(fmt.Sprintf("host-%d.example.com", i))
			if lastErr != nil {
				break
			}
		}
		assert.Error(t, lastErr, "a /30 and a /126 both hold only a handful of addresses")
	})

	t.Run("rejects an invalid range at construction", func(t *testing.T) {
		_, err := newFakeIPPool("not-a-cidr", "100::/64")
		require.Error(t, err)

		_, err = newFakeIPPool("198.18.0.0/15", "not-a-cidr")
		require.Error(t, err)
	})
}

func TestFakeIPPoolLookup(t *testing.T) {
	p, err := newFakeIPPool("198.18.0.0/15", "100::/64")
	require.NoError(t, err)

	addrs, err := p.allocate("api.example.com")
	require.NoError(t, err)

	v4, err := netip.ParseAddr(addrs.V4)
	require.NoError(t, err)
	host, ok := p.lookup(v4)
	require.True(t, ok)
	assert.Equal(t, "api.example.com", host)

	v6, err := netip.ParseAddr(addrs.V6)
	require.NoError(t, err)
	host, ok = p.lookup(v6)
	require.True(t, ok)
	assert.Equal(t, "api.example.com", host)

	_, ok = p.lookup(netip.MustParseAddr("8.8.8.8"))
	assert.False(t, ok, "an address nothing allocated must not match")
}
