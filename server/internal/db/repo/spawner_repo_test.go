package repo_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestSpawnerRepo_CreateAndGet(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	desc := "primary spawner"
	model := "sonnet"
	s, err := r.Create(t.Context(),
		"Claude Default", "claude-default", "claude",
		[]string{"--print"},
		map[string]string{"FOO": "bar"},
		&model, &desc,
		"claude", map[string]string{},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "Claude Default", s.Name)
	require.Equal(t, "claude-default", s.Slug)
	require.Equal(t, "claude", s.Command)
	require.Equal(t, []string{"--print"}, s.Args)
	require.Equal(t, map[string]string{"FOO": "bar"}, s.Env)
	require.Equal(t, "claude", s.AdapterType)
	require.Equal(t, map[string]string{}, s.AdapterConfig)
	require.NotNil(t, s.ModelOverride)
	require.Equal(t, "sonnet", *s.ModelOverride)
	require.True(t, s.BuiltIn)

	got, err := r.GetByID(t.Context(), s.ID)
	require.NoError(t, err)
	require.Equal(t, s.ID, got.ID)

	bySlug, err := r.GetBySlug(t.Context(), "claude-default")
	require.NoError(t, err)
	require.Equal(t, s.ID, bySlug.ID)
}

func TestSpawnerRepo_CreateNormalizesNilArgsAndEnv(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	s, err := r.Create(t.Context(), "X", "x-nil", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)
	require.NotNil(t, s.Args)
	require.NotNil(t, s.Env)
	require.Empty(t, s.Args)
	require.Empty(t, s.Env)
}

func TestSpawnerRepo_CreateWithOllamaAdapterRoundTrips(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	s, err := r.Create(t.Context(), "Ollama", "ollama-spawner", "claude", nil, nil, nil, nil,
		"ollama", map[string]string{"host": "http://x:11434"}, false)
	require.NoError(t, err)
	require.Equal(t, "ollama", s.AdapterType)
	require.Equal(t, map[string]string{"host": "http://x:11434"}, s.AdapterConfig)

	got, err := r.GetByID(t.Context(), s.ID)
	require.NoError(t, err)
	require.Equal(t, "ollama", got.AdapterType)
	require.Equal(t, map[string]string{"host": "http://x:11434"}, got.AdapterConfig)
}

func TestSpawnerRepo_CreateWithOpenAIAdapterRoundTrips(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	cfg := map[string]string{
		"base_url":      "https://api.openai.com/v1",
		"api_key_env":   "OPENAI_API_KEY",
		"default_model": "gpt-4o-mini",
	}
	s, err := r.Create(t.Context(), "OpenAI", "openai-spawner", "claude", nil, nil, nil, nil,
		"openai", cfg, false)
	require.NoError(t, err)
	require.Equal(t, "openai", s.AdapterType)
	require.Equal(t, cfg, s.AdapterConfig)

	got, err := r.GetByID(t.Context(), s.ID)
	require.NoError(t, err)
	require.Equal(t, "openai", got.AdapterType)
	require.Equal(t, cfg, got.AdapterConfig)
}

func TestSpawnerRepo_CreateDefaultsAdapterTypeToClaude(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	s, err := r.Create(t.Context(), "Defaults", "defaults-adapter", "claude", nil, nil, nil, nil,
		"", nil, false)
	require.NoError(t, err)
	require.Equal(t, "claude", s.AdapterType)
	require.Equal(t, map[string]string{}, s.AdapterConfig)
}

func TestSpawnerRepo_SlugUniqueness(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	_, err := r.Create(t.Context(), "A", "dup-slug", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)

	_, err = r.Create(t.Context(), "B", "dup-slug", "claude", nil, nil, nil, nil, "", nil, false)
	require.Error(t, err)
}

func TestSpawnerRepo_List(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	_, err := r.Create(t.Context(), "A", "list-a", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)
	_, err = r.Create(t.Context(), "B", "list-b", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)

	all, err := r.List(t.Context())
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestSpawnerRepo_Update(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	s, err := r.Create(t.Context(), "Old", "upd", "claude", []string{"a"}, map[string]string{"K": "V"}, nil, nil, "", nil, false)
	require.NoError(t, err)

	newName := "New"
	newCmd := "claude-code"
	newDesc := "now with description"
	updated, err := r.Update(t.Context(), s.ID,
		&newName, nil, &newCmd,
		[]string{"b", "c"}, map[string]string{"K2": "V2"},
		nil, &newDesc,
		nil, nil,
		false, false,
	)
	require.NoError(t, err)
	require.Equal(t, "New", updated.Name)
	require.Equal(t, "claude-code", updated.Command)
	require.Equal(t, []string{"b", "c"}, updated.Args)
	require.Equal(t, map[string]string{"K2": "V2"}, updated.Env)
	require.NotNil(t, updated.Description)
	require.Equal(t, "now with description", *updated.Description)

	cleared, err := r.Update(t.Context(), s.ID, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, true)
	require.NoError(t, err)
	require.Nil(t, cleared.Description)

	model := "opus"
	withModel, err := r.Update(t.Context(), s.ID, nil, nil, nil, nil, nil, &model, nil, nil, nil, false, false)
	require.NoError(t, err)
	require.NotNil(t, withModel.ModelOverride)
	require.Equal(t, "opus", *withModel.ModelOverride)

	noModel, err := r.Update(t.Context(), s.ID, nil, nil, nil, nil, nil, nil, nil, nil, nil, true, false)
	require.NoError(t, err)
	require.Nil(t, noModel.ModelOverride)

	// Round-trip adapter_type + adapter_config update.
	newAdapter := "ollama"
	withAdapter, err := r.Update(t.Context(), s.ID, nil, nil, nil, nil, nil, nil, nil, &newAdapter, map[string]string{"host": "http://x:11434"}, false, false)
	require.NoError(t, err)
	require.Equal(t, "ollama", withAdapter.AdapterType)
	require.Equal(t, map[string]string{"host": "http://x:11434"}, withAdapter.AdapterConfig)
}

func TestSpawnerRepo_DeleteBuiltInRejected(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	s, err := r.Create(t.Context(), "BI", "built-in", "claude", nil, nil, nil, nil, "", nil, true)
	require.NoError(t, err)

	err = r.Delete(t.Context(), s.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, repo.ErrSpawnerBuiltIn))
}

func TestSpawnerRepo_SetDefaultEnforcesSingleDefault(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	a, err := r.Create(t.Context(), "A", "a", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)
	b, err := r.Create(t.Context(), "B", "b", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)

	// No default yet.
	_, err = r.GetDefault(t.Context())
	require.Error(t, err)

	newA, prev, err := r.SetDefault(t.Context(), a.ID)
	require.NoError(t, err)
	require.True(t, newA.IsDefault)
	require.Empty(t, prev, "no prior default to report")

	def, err := r.GetDefault(t.Context())
	require.NoError(t, err)
	require.Equal(t, a.ID, def.ID)

	// Switching to b clears a and reports a as the previous default.
	newB, prev, err := r.SetDefault(t.Context(), b.ID)
	require.NoError(t, err)
	require.True(t, newB.IsDefault)
	require.Equal(t, a.ID, prev)

	reloadedA, err := r.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	require.False(t, reloadedA.IsDefault, "former default must be cleared")

	def, err = r.GetDefault(t.Context())
	require.NoError(t, err)
	require.Equal(t, b.ID, def.ID)
}

func TestSpawnerRepo_DeleteDefaultRejected(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	a, err := r.Create(t.Context(), "A", "a", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)
	_, _, err = r.SetDefault(t.Context(), a.ID)
	require.NoError(t, err)

	err = r.Delete(t.Context(), a.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, repo.ErrSpawnerIsDefault))
}

func TestSpawnerRepo_DeleteInUseByTask(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)
	tr := repo.NewTaskRepo(client)

	s, err := r.Create(t.Context(), "T", "in-use-task", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)

	_, err = tr.Create(t.Context(), repo.CreateTaskInput{
		Slug:                "uses-spawner",
		Title:               "Task",
		Cwd:                 "/tmp",
		CurrentStage:        "backlog",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		SpawnerID:           &s.ID,
	})
	require.NoError(t, err)

	err = r.Delete(t.Context(), s.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, repo.ErrSpawnerInUse))
}

func TestSpawnerRepo_DeleteInUseByProject(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)
	pr := repo.NewProjectRepo(client)

	s, err := r.Create(t.Context(), "P", "in-use-project", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)

	_, err = pr.Create(t.Context(), "Proj", "proj-uses-spawner", nil, nil, &s.ID, nil)
	require.NoError(t, err)

	err = r.Delete(t.Context(), s.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, repo.ErrSpawnerInUse))
}

func TestSpawnerRepo_DeleteHappyPath(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewSpawnerRepo(client)

	s, err := r.Create(t.Context(), "Dispose", "dispose", "claude", nil, nil, nil, nil, "", nil, false)
	require.NoError(t, err)

	require.NoError(t, r.Delete(t.Context(), s.ID))

	_, err = r.GetByID(t.Context(), s.ID)
	require.Error(t, err)
}
