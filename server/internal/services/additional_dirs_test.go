package services

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/stretchr/testify/require"
)

func TestAdditionalDirsForProject(t *testing.T) {
	folder := func(path string) *ent.ProjectFolder { return &ent.ProjectFolder{Path: path} }

	tests := []struct {
		name    string
		folders []*ent.ProjectFolder
		cwd     string
		want    []string
	}{
		{
			name:    "empty input returns nil",
			folders: nil,
			cwd:     "/repo",
			want:    nil,
		},
		{
			name:    "empty slice returns nil",
			folders: []*ent.ProjectFolder{},
			cwd:     "/repo",
			want:    nil,
		},
		{
			name:    "folder equal to cwd is excluded",
			folders: []*ent.ProjectFolder{folder("/repo"), folder("/extra")},
			cwd:     "/repo",
			want:    []string{"/extra"},
		},
		{
			name:    "all folders equal to cwd returns nil",
			folders: []*ent.ProjectFolder{folder("/repo")},
			cwd:     "/repo",
			want:    nil,
		},
		{
			name:    "duplicates are collapsed to first occurrence",
			folders: []*ent.ProjectFolder{folder("/a"), folder("/b"), folder("/a")},
			cwd:     "/repo",
			want:    []string{"/a", "/b"},
		},
		{
			name:    "empty path entries are skipped",
			folders: []*ent.ProjectFolder{folder(""), folder("/valid")},
			cwd:     "/repo",
			want:    []string{"/valid"},
		},
		{
			name:    "nil folder entries are skipped",
			folders: []*ent.ProjectFolder{nil, folder("/valid")},
			cwd:     "/repo",
			want:    []string{"/valid"},
		},
		{
			name:    "order is preserved",
			folders: []*ent.ProjectFolder{folder("/z"), folder("/a"), folder("/m")},
			cwd:     "/repo",
			want:    []string{"/z", "/a", "/m"},
		},
		{
			name:    "no extra dirs when only cwd folder present",
			folders: []*ent.ProjectFolder{folder("/repo")},
			cwd:     "/repo",
			want:    nil,
		},
		{
			name: "mixed: cwd excluded, duplicate collapsed, empty skipped",
			folders: []*ent.ProjectFolder{
				folder("/repo"),
				folder("/extra1"),
				folder(""),
				folder("/extra1"),
				folder("/extra2"),
			},
			cwd:  "/repo",
			want: []string{"/extra1", "/extra2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AdditionalDirsForProject(tc.folders, tc.cwd)
			require.Equal(t, tc.want, got)
		})
	}
}
