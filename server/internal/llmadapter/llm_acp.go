package llmadapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/lx-wnk/kontor/server/internal/acp"
)

// acpTurnTimeout bounds one ACP stage. It is longer than the chat-style
// adapters' 5/10-minute budgets because a coding agent's turn covers a whole
// stage — tool calls, edits, and test runs — not one round-trip reply.
const acpTurnTimeout = 30 * time.Minute

// acpConn is the part of an ACP client connection the adapter drives. It exists
// so tests can run the adapter without starting a process.
type acpConn interface {
	Initialize(ctx context.Context, p sdkacp.InitializeRequest) (sdkacp.InitializeResponse, error)
	NewSession(ctx context.Context, p sdkacp.NewSessionRequest) (sdkacp.NewSessionResponse, error)
	SetSessionMode(ctx context.Context, p sdkacp.SetSessionModeRequest) (sdkacp.SetSessionModeResponse, error)
	Prompt(ctx context.Context, p sdkacp.PromptRequest) (sdkacp.PromptResponse, error)
}

// ACPSpawner runs one stage on an agent that speaks the Agent Client Protocol.
// Unlike the subprocess adapters it owns the connection for the whole turn,
// because an ACP agent blocks on permission requests until the client answers.
type ACPSpawner struct {
	Command    string
	Args       []string
	Permission func(context.Context, acp.PermissionRequest) (acp.PermissionDecision, error)

	newConn func(client *acp.Client, in io.Writer, out io.Reader) acpConn
	// starter launches the built command, defaulting to (*exec.Cmd).Start. Test
	// seam: it lets a test run the real construction path in connect() and still
	// decide how the launch ends. A replacement that skips Start also takes over
	// Start's job of releasing the pipes.
	starter func(*exec.Cmd) error
}

func (s *ACPSpawner) Name() string { return "acp" }

func (s *ACPSpawner) Spawn(ctx context.Context, args LLMSpawnArgs) (LLMSpawnResult, error) {
	ctx, cancel := context.WithTimeout(ctx, acpTurnTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return LLMSpawnResult{}, fmt.Errorf("acp adapter: %w", err)
	}

	var mu sync.Mutex
	var reply strings.Builder
	client := &acp.Client{
		OnEvent: func(e acp.Event) {
			if e.Kind != "agent_message" {
				return
			}
			mu.Lock()
			reply.WriteString(e.Text)
			mu.Unlock()
		},
		OnPermission: s.Permission,
	}

	conn, closeConn, childStderr, err := s.connect(ctx, client)
	if err != nil {
		return LLMSpawnResult{}, err
	}
	defer closeConn()

	if _, err := conn.Initialize(ctx, sdkacp.InitializeRequest{
		ProtocolVersion: sdkacp.ProtocolVersionNumber,
	}); err != nil {
		return LLMSpawnResult{}, fmt.Errorf("acp adapter: initialize: %w: stderr: %s", err, childStderr())
	}

	sess, err := conn.NewSession(ctx, sdkacp.NewSessionRequest{
		Cwd: args.WorkDir, McpServers: []sdkacp.McpServer{},
	})
	if err != nil {
		return LLMSpawnResult{}, fmt.Errorf("acp adapter: new session: %w", err)
	}

	// A session that cannot be pinned is a session without a permission gate.
	if err := acp.EnsureMode(ctx, conn, sess.SessionId, sess.Modes, acp.ModeGated); err != nil {
		return LLMSpawnResult{}, fmt.Errorf("acp adapter: %w", err)
	}

	prompt := args.SystemPrompt
	if prompt != "" && args.UserPrompt != "" {
		prompt += "\n\n"
	}
	prompt += args.UserPrompt

	if _, err := conn.Prompt(ctx, sdkacp.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []sdkacp.ContentBlock{sdkacp.TextBlock(prompt)},
	}); err != nil {
		return LLMSpawnResult{}, fmt.Errorf("acp adapter: prompt: %w", err)
	}

	mu.Lock()
	text := reply.String()
	mu.Unlock()

	file, err := writeSyntheticSession(args.WorkDir, args.StageRunID, text)
	if err != nil {
		return LLMSpawnResult{}, fmt.Errorf("acp adapter: %w", err)
	}
	return LLMSpawnResult{PID: 0, SessionID: string(sess.SessionId), SessionFile: file}, nil
}

// connect starts the agent process and returns the driven connection, a
// teardown that stops the process and releases its pipes, and an accessor
// for whatever the child has written to stderr so far.
func (s *ACPSpawner) connect(ctx context.Context, client *acp.Client) (acpConn, func(), func() string, error) {
	noStderr := func() string { return "" }
	if s.newConn != nil {
		return s.newConn(client, io.Discard, strings.NewReader("")), func() {}, noStderr, nil
	}
	if s.Command == "" {
		return nil, nil, nil, fmt.Errorf("acp adapter: no command configured")
	}
	// #nosec G204 -- s.Command/s.Args are unvalidated adapter_config values: ValidateAdapterConfig allow-lists keys and caps value length only, and adapter_factory.go splits args with strings.Fields straight into argv; the writing /api/spawners routes are loopback-only and, with the default auth.mode=none, unauthenticated, so this rests solely on the loopback bind plus the same-origin check, and argv is passed without a shell.
	cmd := exec.CommandContext(ctx, s.Command, s.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderrBuf syncBuffer
	cmd.Stderr = &stderrBuf
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("acp adapter: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, fmt.Errorf("acp adapter: stdout: %w", err)
	}
	start := s.starter
	if start == nil {
		start = (*exec.Cmd).Start
	}
	if err := start(cmd); err != nil {
		// exec.Cmd.Start already closes stdin/stdout when the process fails to launch.
		return nil, nil, nil, fmt.Errorf("acp adapter: start %q: %w: stderr: %s", s.Command, err, stderrBuf.String())
	}
	conn := sdkacp.NewClientSideConnection(client, stdin, stdout)
	teardown := func() {
		_ = stdin.Close()
		signalGroup(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}
	return conn, teardown, func() string { return stderrBuf.String() }, nil
}

// os/exec copies the child's stderr from its own goroutine, and the error paths
// read the buffer before cmd.Wait() has joined it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// signalGroup sends sig to the process group led by pid (Setpgid makes the
// child its own group leader, so its pgid == pid). The negative target
// reaches the leader and all descendants — killing the pid alone would
// orphan them, e.g. the node grandchild the default npx command forks.
// Falls back to the single process if the group send fails.
func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}
