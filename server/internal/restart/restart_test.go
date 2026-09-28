package restart_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/restart"
)

type fakeRestarter struct{ reexec, exit int }

func (f *fakeRestarter) Reexec() error                         { f.reexec++; return nil }
func (f *fakeRestarter) Exit()                                 { f.exit++ }
func (f *fakeRestarter) Build(context.Context) ([]byte, error) { return nil, nil }

func TestExecuteReexecMode(t *testing.T) {
	f := &fakeRestarter{}
	restart.Execute(restart.ModeReexec, f)
	require.Equal(t, 1, f.reexec)
	require.Equal(t, 0, f.exit)
}

func TestExecuteExitMode(t *testing.T) {
	f := &fakeRestarter{}
	restart.Execute(restart.ModeExit, f)
	require.Equal(t, 1, f.exit)
	require.Equal(t, 0, f.reexec)
}

func TestControllerTriggerIsNonBlocking(t *testing.T) {
	c := restart.NewController("reexec")
	c.Trigger() // must not block even with no reader
	c.Trigger() // second send coalesces (buffered size 1)
	select {
	case <-c.C():
	default:
		t.Fatal("expected a pending restart signal")
	}
}
