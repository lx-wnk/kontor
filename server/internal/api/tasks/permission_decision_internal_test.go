package tasks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

const (
	appTool    = "mcp__mail__move_message"
	nonAppTool = "Bash"
)

func routineTask() *ent.Task {
	routineID := "r1"
	return &ent.Task{ID: "t1", RoutineID: &routineID}
}

func nonRoutineTask() *ent.Task {
	return &ent.Task{ID: "t1"}
}

func TestParseDecision(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		outcome  string
		want     string
		wantErr  string
	}{
		{name: "both empty", wantErr: decisionUsage},
		{name: "unknown decision", decision: "bogus", wantErr: decisionUsage},
		{name: "invalid outcome alone", outcome: "maybe", wantErr: "outcome must be granted or denied"},
		{name: "granted aliases to allow_once", outcome: "granted", want: DecisionAllowOnce},
		{name: "denied aliases to deny_once", outcome: "denied", want: DecisionDenyOnce},
		{name: "decision alone", decision: DecisionAllowRoutine, want: DecisionAllowRoutine},
		{name: "agreeing decision and outcome", decision: DecisionAllowOnce, outcome: "granted", want: DecisionAllowOnce},
		{name: "disagreeing decision and outcome", decision: DecisionAllowOnce, outcome: "denied", wantErr: "decision and outcome disagree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDecision(tt.decision, tt.outcome)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestValidateDecision(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		task     *ent.Task
		tools    []string
		wantErr  string
	}{
		{name: "allow_once never validated", decision: DecisionAllowOnce, task: nonRoutineTask(), tools: []string{nonAppTool}},
		{name: "deny_once never validated", decision: DecisionDenyOnce, task: nonRoutineTask(), tools: []string{nonAppTool}},
		{name: "allow_routine without routine", decision: DecisionAllowRoutine, task: nonRoutineTask(), tools: []string{appTool},
			wantErr: "routine decisions need a task started by a routine"},
		{name: "deny_routine without routine", decision: DecisionDenyRoutine, task: nonRoutineTask(), tools: []string{appTool},
			wantErr: "routine decisions need a task started by a routine"},
		{name: "allow_routine non-application tool", decision: DecisionAllowRoutine, task: routineTask(), tools: []string{nonAppTool},
			wantErr: "routine decisions apply to application tools only"},
		{name: "allow_routine application tool on routine task", decision: DecisionAllowRoutine, task: routineTask(), tools: []string{appTool}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDecision(tt.decision, tt.task, tt.tools)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestDecisionGrants(t *testing.T) {
	t.Run("allow_once application tool grants a task-context row", func(t *testing.T) {
		got := decisionGrants(DecisionAllowOnce, nonRoutineTask(), appTool, "human")
		require.Len(t, got, 1)
		require.Equal(t, appTool, got[0].CapabilityName)
		require.Equal(t, repo.GrantContextFor(repo.GrantContextTask, "t1"), got[0].Context)
		require.Equal(t, repo.GrantModeAllow, got[0].Mode)
	})

	t.Run("allow_once non-application tool grants nothing", func(t *testing.T) {
		got := decisionGrants(DecisionAllowOnce, nonRoutineTask(), nonAppTool, "human")
		require.Empty(t, got)
	})

	t.Run("allow_routine grants a routine-context allow row", func(t *testing.T) {
		got := decisionGrants(DecisionAllowRoutine, routineTask(), appTool, "human")
		require.Len(t, got, 1)
		require.Equal(t, repo.GrantContextFor(repo.GrantContextRoutine, "r1"), got[0].Context)
		require.Equal(t, repo.GrantModeAllow, got[0].Mode)
	})

	t.Run("deny_routine grants a routine-context deny row", func(t *testing.T) {
		got := decisionGrants(DecisionDenyRoutine, routineTask(), appTool, "human")
		require.Len(t, got, 1)
		require.Equal(t, repo.GrantContextFor(repo.GrantContextRoutine, "r1"), got[0].Context)
		require.Equal(t, repo.GrantModeDeny, got[0].Mode)
	})

	t.Run("deny_once grants nothing", func(t *testing.T) {
		got := decisionGrants(DecisionDenyOnce, nonRoutineTask(), appTool, "human")
		require.Empty(t, got)
	})
}

func TestDecisionResumePrompt(t *testing.T) {
	tools := []string{appTool}

	require.Equal(t, "", decisionResumePrompt(DecisionAllowOnce, tools))
	require.Equal(t, "", decisionResumePrompt(DecisionAllowRoutine, tools))
	require.Equal(t, deniedResumePrompt(tools), decisionResumePrompt(DecisionDenyOnce, tools))

	got := decisionResumePrompt(DecisionDenyRoutine, tools)
	require.Contains(t, got, appTool)
	require.Contains(t, got, "for this routine")
}
