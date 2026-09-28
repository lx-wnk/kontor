package serverapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildGitHubClient_UnconfiguredIsNotAnError: an absent GitHub
// configuration must leave the rest of the server running, exactly as an
// absent Obsidian vault does.
func TestBuildGitHubClient_UnconfiguredIsNotAnError(t *testing.T) {
	svc := newSettingsServiceForTest(t) // nothing set
	client, err := buildGitHubClient(t.Context(), svc)
	require.NoError(t, err)
	assert.Nil(t, client, "an unconfigured GitHub must disable the feature, not fail the boot")
}

// github.token and github.repos are a required PAIR. Each direction gets its
// own test so a regression in one check cannot hide behind the other passing —
// the same shape TestBuildObsidianClient_Missing* uses for its trio.

func TestBuildGitHubClient_TokenWithoutReposIsAnError(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.token", "ghp_x"))
	_, err := buildGitHubClient(t.Context(), svc)
	require.Error(t, err, "a token with no repositories must fail loudly, not reach every repository the token can see")
	assert.Contains(t, err.Error(), "github.repos")
}

func TestBuildGitHubClient_ReposWithoutTokenIsAnError(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.repos", "lx-wnk/kontor"))
	_, err := buildGitHubClient(t.Context(), svc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github.token")
}

// TestBuildGitHubClient_BaseURLIsNotHalfOfThePair pins the one place this
// differs from Obsidian's trio: github.baseURL carries a registry default, so
// it is never unset and can never be a missing half.
func TestBuildGitHubClient_BaseURLIsNotHalfOfThePair(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.baseURL", "https://api.github.com"))
	client, err := buildGitHubClient(t.Context(), svc)
	require.NoError(t, err, "a base URL alone must not fail the boot — it always has a value")
	assert.Nil(t, client)
}

// TestBuildGitHubClient_ErrorsNeverCarryTheToken: this function reads the
// decrypted secret, so it is the one place a boot error could leak it.
func TestBuildGitHubClient_ErrorsNeverCarryTheToken(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.token", "ghp_supersecret"))
	require.NoError(t, svc.Set(t.Context(), "github.repos", "not-an-owner-name-pair"))
	_, err := buildGitHubClient(t.Context(), svc)
	require.Error(t, err, "a malformed github.repos must fail the boot")
	assert.False(t, strings.Contains(err.Error(), "ghp_supersecret"), "boot error carries the token: %v", err)
}

func TestBuildGitHubClient_FullyConfiguredBuildsAClient(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.token", "ghp_x"))
	require.NoError(t, svc.Set(t.Context(), "github.repos", "lx-wnk/kontor, golang/go"))
	client, err := buildGitHubClient(t.Context(), svc)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, []string{"lx-wnk/kontor", "golang/go"}, client.Repos(),
		"the allow-list must keep its configured order — the summary panel lists repositories in it")
}

// With github.tokenSource = gh-cli the token is not a setting at all, so the
// pair rule changes shape: github.repos alone decides whether the integration
// was asked for, and github.token is never read.

func TestBuildGitHubClient_GhCLIWithoutReposStaysDisabledAndNeverRunsGh(t *testing.T) {
	called := false
	restore := stubGhToken(func(context.Context) (string, error) {
		called = true
		return "tok", nil
	})
	defer restore()

	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.tokenSource", "gh-cli"))

	client, err := buildGitHubClient(t.Context(), svc)
	require.NoError(t, err)
	assert.Nil(t, client)
	assert.False(t, called, "an install that never asked for GitHub must boot without consulting gh")
}

func TestBuildGitHubClient_GhCLIProvidesTheToken(t *testing.T) {
	restore := stubGhToken(func(context.Context) (string, error) { return "gho_from_cli", nil })
	defer restore()

	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.tokenSource", "gh-cli"))
	require.NoError(t, svc.Set(t.Context(), "github.repos", "lx-wnk/Agent-Dashboard"))

	client, err := buildGitHubClient(t.Context(), svc)
	require.NoError(t, err)
	require.NotNil(t, client, "repos plus a gh token is a complete configuration")
}

// A logged-out or absent gh must fail the boot loudly rather than start a
// server whose GitHub calls will all answer 401.
func TestBuildGitHubClient_GhCLIFailureNamesTheSource(t *testing.T) {
	restore := stubGhToken(func(context.Context) (string, error) {
		return "", errors.New("gh auth token failed: To get started with GitHub CLI, please run: gh auth login")
	})
	defer restore()

	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.tokenSource", "gh-cli"))
	require.NoError(t, svc.Set(t.Context(), "github.repos", "lx-wnk/Agent-Dashboard"))

	_, err := buildGitHubClient(t.Context(), svc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gh-cli", "the error must name the source that was configured")
	assert.Contains(t, err.Error(), "gh auth login", "gh's own advice is the useful half")
}

// The default source is unchanged: a stored token still works exactly as before.
func TestBuildGitHubClient_DefaultSourceStillUsesTheStoredToken(t *testing.T) {
	restore := stubGhToken(func(context.Context) (string, error) {
		t.Fatal("gh must not be consulted when tokenSource is the default")
		return "", nil
	})
	defer restore()

	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "github.token", "ghp_x"))
	require.NoError(t, svc.Set(t.Context(), "github.repos", "lx-wnk/Agent-Dashboard"))

	client, err := buildGitHubClient(t.Context(), svc)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func stubGhToken(fn func(context.Context) (string, error)) func() {
	prev := ghTokenFn
	ghTokenFn = fn
	return func() { ghTokenFn = prev }
}
