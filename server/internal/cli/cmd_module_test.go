package cli

import "testing"

// A trailing @ref names a version; the @ in an scp-style address does not. Get
// this wrong and `git@github.com:lx-wnk/kontor.git` installs from the host
// "git" at ref "github.com:lx-wnk/kontor.git".
func TestSplitModuleRef(t *testing.T) {
	cases := []struct{ arg, url, ref string }{
		{"https://github.com/lx-wnk/mod.git", "https://github.com/lx-wnk/mod.git", ""},
		{"https://github.com/lx-wnk/mod.git@v1.2.0", "https://github.com/lx-wnk/mod.git", "v1.2.0"},
		{"git@github.com:lx-wnk/mod.git", "git@github.com:lx-wnk/mod.git", ""},
		{"git@github.com:lx-wnk/mod.git@v1.2.0", "git@github.com:lx-wnk/mod.git", "v1.2.0"},
		{"file:///tmp/mod@main", "file:///tmp/mod", "main"},
	}
	for _, c := range cases {
		url, ref := splitModuleRef(c.arg)
		if url != c.url || ref != c.ref {
			t.Errorf("splitModuleRef(%q) = (%q, %q), want (%q, %q)", c.arg, url, ref, c.url, c.ref)
		}
	}
}

func TestModuleDirName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/lx-wnk/mod.git": "mod",
		"git@github.com:lx-wnk/mod.git":     "mod",
		"file:///tmp/my-module":             "my-module",
	}
	for url, want := range cases {
		if got := moduleDirName(url); got != want {
			t.Errorf("moduleDirName(%q) = %q, want %q", url, got, want)
		}
	}
}
