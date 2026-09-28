package proc_test

import (
	"os"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/proc"
	"github.com/stretchr/testify/require"
)

func TestIsPidAlive_Self(t *testing.T) {
	require.True(t, proc.IsPidAlive(os.Getpid()))
}

func TestIsPidAlive_Zero(t *testing.T) {
	require.False(t, proc.IsPidAlive(0))
}

func TestIsPidAlive_Negative(t *testing.T) {
	require.False(t, proc.IsPidAlive(-1))
}

func TestIsPidAlive_DeadPID(t *testing.T) {
	require.False(t, proc.IsPidAlive(999999))
}

func TestIsPidAlive_BogusPID(t *testing.T) {
	require.False(t, proc.IsPidAlive(1<<30))
}
