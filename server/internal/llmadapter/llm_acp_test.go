package llmadapter

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/lx-wnk/kontor/server/internal/acp"
	"github.com/stretchr/testify/require"
)

// fakeConn stands in for a live ACP connection so no process is started.
type fakeConn struct {
	client    *acp.Client
	modes     *sdkacp.SessionModeState
	setModes  []sdkacp.SessionModeId
	gotPrompt []sdkacp.ContentBlock
	initErr   error
	newErr    error
	promptErr error
	reply     string
}

func (f *fakeConn) Initialize(ctx context.Context, p sdkacp.InitializeRequest) (sdkacp.InitializeResponse, error) {
	return sdkacp.InitializeResponse{ProtocolVersion: sdkacp.ProtocolVersionNumber}, f.initErr
}

func (f *fakeConn) NewSession(ctx context.Context, p sdkacp.NewSessionRequest) (sdkacp.NewSessionResponse, error) {
	return sdkacp.NewSessionResponse{SessionId: "sess-1", Modes: f.modes}, f.newErr
}

func (f *fakeConn) SetSessionMode(ctx context.Context, p sdkacp.SetSessionModeRequest) (sdkacp.SetSessionModeResponse, error) {
	f.setModes = append(f.setModes, p.ModeId)
	return sdkacp.SetSessionModeResponse{}, nil
}

func (f *fakeConn) Prompt(ctx context.Context, p sdkacp.PromptRequest) (sdkacp.PromptResponse, error) {
	f.gotPrompt = p.Prompt
	if f.promptErr != nil {
		return sdkacp.PromptResponse{}, f.promptErr
	}
	_ = f.client.SessionUpdate(ctx, sdkacp.SessionNotification{
		SessionId: "sess-1",
		Update: sdkacp.SessionUpdate{
			AgentMessageChunk: &sdkacp.SessionUpdateAgentMessageChunk{Content: sdkacp.TextBlock(f.reply)},
		},
	})
	return sdkacp.PromptResponse{StopReason: sdkacp.StopReasonEndTurn}, nil
}

func gatedModes() *sdkacp.SessionModeState {
	return &sdkacp.SessionModeState{
		CurrentModeId:  "auto",
		AvailableModes: []sdkacp.SessionMode{{Id: "auto", Name: "auto"}, {Id: acp.ModeGated, Name: "default"}},
	}
}

// spawnerWith wires an ACPSpawner to a fakeConn, bypassing process start.
func spawnerWith(t *testing.T, f *fakeConn) *ACPSpawner {
	t.Helper()
	s := &ACPSpawner{Command: "true"}
	s.newConn = func(c *acp.Client, _ io.Writer, _ io.Reader) acpConn {
		f.client = c
		return f
	}
	return s
}

func testArgs(t *testing.T) LLMSpawnArgs {
	t.Helper()
	return LLMSpawnArgs{
		TaskID: "task-1", StageRunID: "sr-1", Stage: "review",
		SystemPrompt: "sys", UserPrompt: "do the thing", WorkDir: t.TempDir(),
	}
}

func TestACPSpawnerName(t *testing.T) {
	require.Equal(t, "acp", (&ACPSpawner{}).Name())
}

func TestACPSpawnerWritesTheAgentReplyToASessionFile(t *testing.T) {
	f := &fakeConn{modes: gatedModes(), reply: "the answer"}
	res, err := spawnerWith(t, f).Spawn(context.Background(), testArgs(t))

	require.NoError(t, err)
	require.Equal(t, 0, res.PID, "the adapter owns the process, so it reports no PID")
	require.NotEmpty(t, res.SessionFile)

	b, readErr := os.ReadFile(res.SessionFile)
	require.NoError(t, readErr)
	require.Contains(t, string(b), "the answer")
}

func TestACPSpawnerPinsTheGatedMode(t *testing.T) {
	f := &fakeConn{modes: gatedModes(), reply: "ok"}
	_, err := spawnerWith(t, f).Spawn(context.Background(), testArgs(t))

	require.NoError(t, err)
	require.Equal(t, []sdkacp.SessionModeId{acp.ModeGated}, f.setModes)
}

func TestACPSpawnerRefusesASessionItCannotGate(t *testing.T) {
	f := &fakeConn{
		modes: &sdkacp.SessionModeState{CurrentModeId: "auto",
			AvailableModes: []sdkacp.SessionMode{{Id: "auto", Name: "auto"}}},
		reply: "ok",
	}
	_, err := spawnerWith(t, f).Spawn(context.Background(), testArgs(t))

	require.Error(t, err, "an ungatable session must not run")
	require.Empty(t, f.setModes)
}

func TestACPSpawnerFailsWhenTheSessionCannotStart(t *testing.T) {
	f := &fakeConn{modes: gatedModes(), newErr: errors.New("refused")}
	_, err := spawnerWith(t, f).Spawn(context.Background(), testArgs(t))
	require.Error(t, err)
}

func TestACPSpawnerFailsWhenThePromptFails(t *testing.T) {
	f := &fakeConn{modes: gatedModes(), promptErr: errors.New("model down")}
	_, err := spawnerWith(t, f).Spawn(context.Background(), testArgs(t))
	require.Error(t, err)
}

// promptText reads the text out of a recorded prompt the same way client.go
// does: Text is a pointer, so it must be nil-checked rather than dereferenced
// blindly.
func promptText(t *testing.T, blocks []sdkacp.ContentBlock) string {
	t.Helper()
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	return blocks[0].Text.Text
}

func TestACPSpawnerSendsBothPromptParts(t *testing.T) {
	f := &fakeConn{modes: gatedModes(), reply: "ok"}
	args := testArgs(t)
	_, err := spawnerWith(t, f).Spawn(context.Background(), args)
	require.NoError(t, err)

	require.Equal(t, args.SystemPrompt+"\n\n"+args.UserPrompt, promptText(t, f.gotPrompt))
}

func TestACPSpawnerRefusesAnAlreadyExpiredContext(t *testing.T) {
	f := &fakeConn{modes: gatedModes(), reply: "ok"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := spawnerWith(t, f).Spawn(ctx, testArgs(t))

	require.Error(t, err)
	require.Empty(t, f.setModes, "an expired context must not let the turn proceed")
}

func TestSyncBufferIsSafeForConcurrentWriteAndString(t *testing.T) {
	var b syncBuffer
	const writers = 20
	const chunk = "x"

	var wg sync.WaitGroup
	wg.Add(writers + 1)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			_, err := b.Write([]byte(chunk))
			require.NoError(t, err)
		}()
	}
	go func() {
		defer wg.Done()
		_ = b.String()
	}()
	wg.Wait()

	require.Len(t, b.String(), writers*len(chunk))
}

// The tests below drive the real connect() path — command construction, pipes,
// launch — and only control the launch outcome through the starter seam.

func TestACPSpawnerBuildsTheCommandFromItsConfiguration(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "acp-agent")
	var got *exec.Cmd
	s := &ACPSpawner{Command: bin, Args: []string{"--experimental-acp", "-v"}}
	s.starter = func(cmd *exec.Cmd) error {
		got = cmd
		return errors.New("not launched")
	}

	_, err := s.Spawn(context.Background(), testArgs(t))
	require.Error(t, err)

	require.NotNil(t, got)
	require.Equal(t, bin, got.Path)
	require.Equal(t, []string{bin, "--experimental-acp", "-v"}, got.Args)
	// The agent inherits the server's environment and working directory; the
	// stage's own workdir reaches it through NewSessionRequest.Cwd instead.
	require.Nil(t, got.Env)
	require.Empty(t, got.Dir)
	require.True(t, got.SysProcAttr.Setpgid, "the child leads its own group so teardown reaches its descendants")
	require.NotNil(t, got.Stdin, "ACP framing needs both pipes")
	require.NotNil(t, got.Stdout)
	require.NotNil(t, got.Stderr)
}

func TestACPSpawnerReportsWhatFailedToStart(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "acp-agent")
	launchErr := errors.New("permission denied")
	s := &ACPSpawner{Command: bin}
	s.starter = func(cmd *exec.Cmd) error {
		_, _ = cmd.Stderr.Write([]byte("agent: cannot exec"))
		return launchErr
	}

	_, err := s.Spawn(context.Background(), testArgs(t))

	require.ErrorIs(t, err, launchErr)
	require.Contains(t, err.Error(), bin, "the message must name the binary that failed")
	require.Contains(t, err.Error(), "agent: cannot exec", "the child's stderr is the only clue why")
}

func TestACPSpawnerFailsOnAMissingBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "not-installed")

	_, err := (&ACPSpawner{Command: bin}).Spawn(context.Background(), testArgs(t))

	require.Error(t, err)
	require.Contains(t, err.Error(), bin)
}

func TestACPSpawnerFailsWithoutACommand(t *testing.T) {
	_, err := (&ACPSpawner{}).Spawn(context.Background(), testArgs(t))
	require.Error(t, err)
}

// lowestFreeFD reports the descriptor a fresh open lands on. open(2) always
// takes the lowest free number, so a leaked pipe pushes it up.
func lowestFreeFD(t *testing.T) int {
	t.Helper()
	f, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	return int(f.Fd())
}

func TestACPSpawnerReleasesThePipesWhenTheLaunchFails(t *testing.T) {
	s := &ACPSpawner{Command: filepath.Join(t.TempDir(), "not-installed")}
	args := testArgs(t)
	_, err := s.Spawn(context.Background(), args)
	require.Error(t, err)

	before := lowestFreeFD(t)
	for i := 0; i < 20; i++ {
		_, spawnErr := s.Spawn(context.Background(), args)
		require.Error(t, spawnErr)
	}

	require.LessOrEqual(t, lowestFreeFD(t), before+4, "a failed launch must not keep its stdin/stdout pipes")
}

func TestACPSpawnerKillsTheAgentWhenTheContextIsCancelled(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary on PATH")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var child *exec.Cmd
	s := &ACPSpawner{Command: sleep, Args: []string{"60"}}
	s.starter = func(cmd *exec.Cmd) error {
		child = cmd
		if startErr := cmd.Start(); startErr != nil {
			return startErr
		}
		cancel() // the turn is abandoned while the agent still holds the pipes
		return nil
	}

	_, spawnErr := s.Spawn(ctx, testArgs(t))

	require.Error(t, spawnErr)
	require.NotNil(t, child.ProcessState, "the agent must be reaped, not left as a zombie")
	require.False(t, child.ProcessState.Exited(), "a cancelled turn kills the agent, it never asks it to stop")
	status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.Equal(t, syscall.SIGKILL, status.Signal())
}

func TestACPSpawnerOmitsTheSeparatorWhenThereIsNoSystemPrompt(t *testing.T) {
	f := &fakeConn{modes: gatedModes(), reply: "ok"}
	args := testArgs(t)
	args.SystemPrompt = ""
	_, err := spawnerWith(t, f).Spawn(context.Background(), args)
	require.NoError(t, err)

	require.Equal(t, args.UserPrompt, promptText(t, f.gotPrompt))
}
