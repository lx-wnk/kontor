package repo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newGrantRepo(t *testing.T) (repo.GrantRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewGrantRepo(bundle.Client), context.Background()
}

func TestGrantRequiresGrantedBy(t *testing.T) {
	r, ctx := newGrantRepo(t)
	_, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeAllow,
		Pattern:        "git status*",
		// GrantedBy deliberately omitted
	})
	if err == nil {
		t.Fatal("a grant without granted_by must be refused — identity on a decision is not optional")
	}
}

func TestGrantRevokeIsATombstone(t *testing.T) {
	r, ctx := newGrantRepo(t)
	g, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeAllow,
		Pattern:        "git status*",
		GrantedBy:      "user:alex",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.Revoke(ctx, g.ID, "user:sam"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	all, err := r.ListForCapability(ctx, "Bash")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("revoke must not delete the row, got %d rows", len(all))
	}
	if all[0].RevokedAt == nil {
		t.Error("revoked_at must be set — revocation is a tombstone, not a delete")
	}
	if all[0].RevokedBy != "user:sam" {
		t.Errorf("revoked_by = %q, want %q — a revocation is a security decision and needs an actor", all[0].RevokedBy, "user:sam")
	}
}

func TestGrantRevokeRefusesSecondCall(t *testing.T) {
	r, ctx := newGrantRepo(t)
	g, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeAllow,
		Pattern:        "git status*",
		GrantedBy:      "user:alex",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.Revoke(ctx, g.ID, "user:sam"); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := r.Revoke(ctx, g.ID, "user:pat"); err == nil {
		t.Fatal("a second revoke of an already-revoked grant must be refused — it would overwrite who revoked it")
	}
	all, err := r.ListForCapability(ctx, "Bash")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d rows, want 1", len(all))
	}
	if all[0].RevokedBy != "user:sam" {
		t.Errorf("revoked_by = %q after a refused second revoke, want unchanged %q", all[0].RevokedBy, "user:sam")
	}
}

func TestGrantRevokeRequiresRevokedBy(t *testing.T) {
	r, ctx := newGrantRepo(t)
	g, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeAllow,
		Pattern:        "git status*",
		GrantedBy:      "user:alex",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.Revoke(ctx, g.ID, ""); err == nil {
		t.Fatal("a revoke without revoked_by must be refused — identity on a decision is not optional")
	}
}

func TestGrantCreateRejectsInvalidPattern(t *testing.T) {
	r, ctx := newGrantRepo(t)
	_, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "mail.send",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeAllow,
		Pattern:        "domain:",
		GrantedBy:      "user:alex",
	})
	if err == nil {
		t.Fatal("a grant with an invalid pattern must be refused — a malformed pattern that is stored and never matches is a silent deny")
	}
}

func TestGrantCreateRejectsInvalidMode(t *testing.T) {
	r, ctx := newGrantRepo(t)
	_, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           "alow",
		Pattern:        "git status*",
		GrantedBy:      "user:alex",
	})
	if err == nil {
		t.Fatal("a grant with an invalid mode must be refused — Decide would otherwise silently drop it")
	}
	if !strings.Contains(err.Error(), "deny") || !strings.Contains(err.Error(), "allow") || !strings.Contains(err.Error(), "ask") {
		t.Errorf("error %q must name the valid modes", err.Error())
	}
}

func TestGrantCreateRejectsInvalidContextKind(t *testing.T) {
	r, ctx := newGrantRepo(t)
	_, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor("tsak", ""),
		Mode:           repo.GrantModeAllow,
		Pattern:        "git status*",
		GrantedBy:      "user:alex",
	})
	if err == nil {
		t.Fatal("a grant with an invalid context kind must be refused — Decide would otherwise silently drop it")
	}
	for _, kind := range capability.ContextKinds() {
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error %q must name valid context kind %q", err.Error(), kind)
		}
	}
}

func TestGrantCreateRejectsRefOnGlobalContext(t *testing.T) {
	r, ctx := newGrantRepo(t)
	_, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, "something"),
		Mode:           repo.GrantModeAllow,
		Pattern:        "git status*",
		GrantedBy:      "user:alex",
	})
	if err == nil {
		t.Fatal("a global-context grant with a non-empty ref must be refused — Decide looks up Context{Kind, Ref} exactly and the row would never match")
	}
}

func TestGrantCreateRejectsEmptyRefOnNonGlobalContext(t *testing.T) {
	kinds := []string{
		repo.GrantContextProject,
		repo.GrantContextTask,
		repo.GrantContextRoutine,
		repo.GrantContextApplication,
		repo.GrantContextAgentSession,
	}
	for _, kind := range kinds {
		r, ctx := newGrantRepo(t)
		_, err := r.Create(ctx, repo.CreateGrantInput{
			CapabilityName: "Bash",
			Context:        repo.GrantContextFor(kind, ""),
			Mode:           repo.GrantModeAllow,
			Pattern:        "git status*",
			GrantedBy:      "user:alex",
		})
		if err == nil {
			t.Errorf("Create with context kind %q and empty ref must be refused — repo.Scope.Normalize collapses it to global and the grant can never match", kind)
			continue
		}
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error %q must name context kind %q", err.Error(), kind)
		}
	}
}

func TestGrantCreateAcceptsEveryValidMode(t *testing.T) {
	for _, mode := range capability.Modes() {
		r, ctx := newGrantRepo(t)
		_, err := r.Create(ctx, repo.CreateGrantInput{
			CapabilityName: "Bash",
			Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
			Mode:           mode,
			Pattern:        "git status*",
			GrantedBy:      "user:alex",
		})
		if err != nil {
			t.Errorf("Create with mode %q: %v", mode, err)
		}
	}
}

func TestGrantCreateAcceptsEveryValidContextKind(t *testing.T) {
	for _, kind := range capability.ContextKinds() {
		r, ctx := newGrantRepo(t)
		ref := ""
		if kind != repo.GrantContextGlobal {
			ref = "some-ref"
		}
		_, err := r.Create(ctx, repo.CreateGrantInput{
			CapabilityName: "Bash",
			Context:        repo.GrantContextFor(kind, ref),
			Mode:           repo.GrantModeAllow,
			Pattern:        "git status*",
			GrantedBy:      "user:alex",
		})
		if err != nil {
			t.Errorf("Create with context kind %q: %v", kind, err)
		}
	}
}

// TestGrantModeAndContextConstantsMatchCapability guards against repo's
// constants and Decide's contextRank/modeRank maps drifting apart: a mode or
// context kind repo considers valid but capability does not would be written
// successfully and then silently ignored by Decide.
func TestGrantModeAndContextConstantsMatchCapability(t *testing.T) {
	modes := []string{repo.GrantModeAllow, repo.GrantModeDeny, repo.GrantModeAsk}
	for _, mode := range modes {
		if !capability.IsValidMode(mode) {
			t.Errorf("repo mode constant %q is not accepted by capability.IsValidMode", mode)
		}
	}
	kinds := []string{
		repo.GrantContextAgentSession,
		repo.GrantContextTask,
		repo.GrantContextRoutine,
		repo.GrantContextApplication,
		repo.GrantContextProject,
		repo.GrantContextGlobal,
	}
	for _, kind := range kinds {
		if !capability.IsValidContextKind(kind) {
			t.Errorf("repo context constant %q is not accepted by capability.IsValidContextKind", kind)
		}
	}
}

func TestGrantListReturnsNewestFirstAcrossCapabilities(t *testing.T) {
	r, ctx := newGrantRepo(t)
	first, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "Bash",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeAllow,
		Pattern:        "git status*",
		GrantedBy:      "user:alex",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := r.Create(ctx, repo.CreateGrantInput{
		CapabilityName: "mail.send",
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeDeny,
		Pattern:        "",
		GrantedBy:      "user:alex",
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	all, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(all))
	}
	if all[0].ID != second.ID || all[1].ID != first.ID {
		t.Fatalf("List order = [%s, %s], want newest first [%s, %s]", all[0].ID, all[1].ID, second.ID, first.ID)
	}
}
