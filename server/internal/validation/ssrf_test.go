package validation_test

import (
	"net"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/validation"
	"github.com/stretchr/testify/require"
)

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		blocked bool
	}{
		// IPv4 loopback
		{"IPv4 loopback 127.0.0.1", "127.0.0.1", true},
		// IPv6 loopback
		{"IPv6 loopback ::1", "::1", true},
		// Link-local
		{"IPv4 link-local 169.254.1.1", "169.254.1.1", true},
		{"IPv6 link-local fe80::1", "fe80::1", true},
		// RFC-1918 private ranges
		{"RFC-1918 10.0.0.1", "10.0.0.1", true},
		{"RFC-1918 172.16.0.1", "172.16.0.1", true},
		{"RFC-1918 192.168.1.1", "192.168.1.1", true},
		// CGNAT (100.64.0.0/10)
		{"CGNAT start 100.64.0.0", "100.64.0.0", true},
		{"CGNAT middle 100.100.0.1", "100.100.0.1", true},
		{"CGNAT end 100.127.255.255", "100.127.255.255", true},
		// Unspecified
		{"unspecified 0.0.0.0", "0.0.0.0", true},
		// Multicast
		{"multicast 224.0.0.1", "224.0.0.1", true},
		// IPv6 unique-local (fc00::/7, RFC 4193) — blocked via IsPrivate
		{"IPv6 ULA fd00::1", "fd00::1", true},
		// Public — NOT blocked
		{"public 8.8.8.8", "8.8.8.8", false},
		{"public 1.1.1.1", "1.1.1.1", false},
		{"just above CGNAT 100.128.0.1", "100.128.0.1", false},
		{"just below CGNAT 100.63.255.255", "100.63.255.255", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			require.NotNil(t, ip, "ParseIP returned nil for %q", tc.ip)
			got := validation.IsBlockedIP(ip)
			if tc.blocked {
				require.True(t, got, "expected %s to be blocked", tc.ip)
			} else {
				require.False(t, got, "expected %s to NOT be blocked", tc.ip)
			}
		})
	}
}
