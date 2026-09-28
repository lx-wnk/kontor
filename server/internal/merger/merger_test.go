package merger_test

import (
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/stretchr/testify/require"
)

func TestCalculateStatus(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		lastActivity time.Time
		want         sdk.AgentStatus
	}{
		{"active: 10s ago", now.Add(-10 * time.Second), sdk.AgentStatusActive},
		{"waiting: 2min ago", now.Add(-2 * time.Minute), sdk.AgentStatusWaiting},
		{"idle: 10min ago", now.Add(-10 * time.Minute), sdk.AgentStatusIdle},
		{"boundary active: 29s ago", now.Add(-29 * time.Second), sdk.AgentStatusActive},
		{"boundary waiting: 4min59s ago", now.Add(-4*time.Minute - 59*time.Second), sdk.AgentStatusWaiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := merger.CalculateStatus(tt.lastActivity)
			require.Equal(t, tt.want, got)
		})
	}
}
