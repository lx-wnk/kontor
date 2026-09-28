package proc_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/proc"
	"github.com/stretchr/testify/require"
)

func TestParentPID_ReportsTheSpawningProcess(t *testing.T) {
	sleeper := exec.Command("sleep", "5")
	require.NoError(t, sleeper.Start())
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })

	ppid, err := proc.ParentPID(sleeper.Process.Pid)
	require.NoError(t, err)
	require.Equal(t, os.Getpid(), ppid)
}

func TestParentPID_DeadPID(t *testing.T) {
	_, err := proc.ParentPID(999999)
	require.Error(t, err)
}

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
