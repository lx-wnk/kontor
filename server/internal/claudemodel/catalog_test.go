package claudemodel

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestLatest_ComparesVersionsNumerically(t *testing.T) {
	saved := ids
	t.Cleanup(func() { ids = saved })
	ids = map[string]bool{
		"claude-opus-4-8":   true,
		"claude-opus-4-10":  true,
		"claude-sonnet-5":   true,
		"claude-sonnet-5-1": true,
		"claude-haiku-4-5":  true,
	}

	cases := map[string]string{
		Opus:   "claude-opus-4-10",
		Sonnet: "claude-sonnet-5-1",
		Haiku:  "claude-haiku-4-5",
	}
	for series, want := range cases {
		if got := Latest(series); got != want {
			t.Errorf("Latest(%q) = %q, want %q", series, got, want)
		}
	}
}

func TestLatest_PanicsWithoutAModelInTheSeries(t *testing.T) {
	saved := ids
	t.Cleanup(func() { ids = saved })
	ids = map[string]bool{"claude-opus-5": true}

	defer func() {
		if recover() == nil {
			t.Fatal("Latest(haiku) with no haiku model must panic, not return a default")
		}
	}()
	Latest(Haiku)
}

// The SPA offers AVAILABLE_MODELS as stage-model choices and the pipeline
// config API rejects anything unknown here, so a model present on only one
// side is either unselectable or selectable-then-refused with a 400.
func TestIDs_MatchFrontendList(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "src", "utils", "models.ts"))
	if err != nil {
		t.Fatalf("read src/utils/models.ts: %v", err)
	}
	block := regexp.MustCompile(`(?s)AVAILABLE_MODELS = \[(.*?)\] as const`).FindSubmatch(src)
	if block == nil {
		t.Fatal("AVAILABLE_MODELS array not found in src/utils/models.ts")
	}
	var frontend []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(block[1], -1) {
		frontend = append(frontend, string(m[1]))
	}
	slices.Sort(frontend)

	if !slices.Equal(frontend, IDs()) {
		t.Fatalf("model lists drifted:\n  src/utils/models.ts: %v\n  claudemodel:         %v", frontend, IDs())
	}
}

// The anthropic-spawner plugin is its own Go module and cannot import this
// package, so its fallback model is a literal that must track the newest Opus.
func TestAnthropicSpawnerDefault_IsLatestOpus(t *testing.T) {
	// Reaches across a module boundary on purpose, and tolerates the module
	// being absent: a module may live in its own repository, and this suite
	// must not fail because one that happens to be in-tree today is not.
	path := filepath.Join("..", "..", "..", "plugins", "anthropic-spawner", "main.go")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skip("anthropic-spawner is not in this tree")
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := regexp.MustCompile(`const defaultModel = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("defaultModel constant not found in plugins/anthropic-spawner/main.go")
	}
	if got, want := string(m[1]), Latest(Opus); got != want {
		t.Fatalf("anthropic-spawner defaultModel = %q, want the newest Opus %q", got, want)
	}
}

// A default model must follow the newest model of its series, so production
// code may name a concrete Claude model ID only in the files that own the
// catalog, the price table, and the one fallback that cannot import this
// package. Anything else derives it through Latest (Go) or latestModel (TS).
func TestNoPinnedModelIDsOutsideTheCatalog(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := map[string]bool{
		"server/internal/claudemodel/catalog.go": true,
		"server/internal/pricing/pricing.go":     true,
		"src/utils/models.ts":                    true,
		"plugins/anthropic-spawner/main.go":      true,
	}
	pinned := regexp.MustCompile(`["'` + "`" + `]claude-(opus|sonnet|haiku|fable)-\d`)
	skipDirs := map[string]bool{"node_modules": true, "dist": true, "ent": true, "testdata": true, "__tests__": true, ".git": true}

	var offenders []string
	for _, dir := range []string{"server", "sdk", "plugins", "desktop", "src"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			isSource := strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".vue")
			isTest := strings.HasSuffix(name, "_test.go") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.")
			if !isSource || isTest {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if allowed[rel] {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if pinned.Match(src) {
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("pinned Claude model IDs in production code — derive them with claudemodel.Latest or latestModel instead:\n  %s", strings.Join(offenders, "\n  "))
	}
}
