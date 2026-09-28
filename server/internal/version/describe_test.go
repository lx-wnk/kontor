package version_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/version"
)

// Every caller has a working answer for "unknown", so a directory that is not
// a repository must come back empty rather than erroring or blocking.
func TestDescribeInIsEmptyOutsideARepository(t *testing.T) {
	if got := version.DescribeIn(context.Background(), t.TempDir()); got != "" {
		t.Fatalf("DescribeIn(non-repo) = %q, want empty", got)
	}
}

// Inside this repository it must name something -- if it came back empty here,
// the staleness check and the rebuild stamp would both silently do nothing.
func TestDescribeInNamesARevisionInsideTheRepository(t *testing.T) {
	if got := version.DescribeIn(context.Background(), "."); got == "" {
		t.Skip("not a git checkout, or git is unavailable")
	}
}
