//go:build linux

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSwapPort(t *testing.T) {
	tests := []struct {
		name string
		in   uint16
		want uint16
	}{
		{name: "port 443 as read from a big-endian wire value", in: 0xbb01, want: 443},
		{name: "port 80", in: 0x5000, want: 80},
		{name: "a high port", in: 0x0b39, want: 14603},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, swapPort(tt.in))
			// swapPort is its own inverse.
			assert.Equal(t, tt.in, swapPort(swapPort(tt.in)))
		})
	}
}
