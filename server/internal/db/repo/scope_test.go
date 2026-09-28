package repo_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestScopeNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   repo.Scope
		want repo.Scope
	}{
		{
			name: "global keeps the empty sentinel",
			in:   repo.Scope{Kind: repo.ScopeGlobal, Ref: ""},
			want: repo.Scope{Kind: repo.ScopeGlobal, Ref: ""},
		},
		{
			name: "global discards a stray ref",
			in:   repo.Scope{Kind: repo.ScopeGlobal, Ref: "/some/project"},
			want: repo.Scope{Kind: repo.ScopeGlobal, Ref: ""},
		},
		{
			name: "project keeps its ref",
			in:   repo.Scope{Kind: repo.ScopeProject, Ref: "/some/project"},
			want: repo.Scope{Kind: repo.ScopeProject, Ref: "/some/project"},
		},
		{
			name: "project with an empty ref collapses to global",
			in:   repo.Scope{Kind: repo.ScopeProject, Ref: ""},
			want: repo.Scope{Kind: repo.ScopeGlobal, Ref: ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Normalize(); got != tt.want {
				t.Errorf("Normalize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestScopeValidate(t *testing.T) {
	if err := repo.GlobalScope().Validate(); err != nil {
		t.Errorf("global scope must validate, got %v", err)
	}
	if err := repo.ProjectScope("/x").Validate(); err != nil {
		t.Errorf("project scope must validate, got %v", err)
	}
	if err := (repo.Scope{Kind: "nonsense", Ref: ""}).Validate(); err == nil {
		t.Error("unknown scope kind must not validate")
	}
}

func TestScopeIsGlobal(t *testing.T) {
	if !repo.GlobalScope().IsGlobal() {
		t.Error("GlobalScope must report IsGlobal")
	}
	if repo.ProjectScope("/x").IsGlobal() {
		t.Error("project scope must not report IsGlobal")
	}
}
