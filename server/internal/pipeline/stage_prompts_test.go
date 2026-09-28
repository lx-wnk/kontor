package pipeline_test

import (
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func TestBuildFeedbackPrefix_NilInput(t *testing.T) {
	result := pipeline.BuildFeedbackPrefix(nil)
	require.Equal(t, "", result)
}

func TestBuildFeedbackPrefix_NoValidationError(t *testing.T) {
	result := pipeline.BuildFeedbackPrefix(map[string]any{
		"some_other_key": "value",
	})
	require.Equal(t, "", result)
}

func TestBuildFeedbackPrefix_ShortOutput(t *testing.T) {
	result := pipeline.BuildFeedbackPrefix(map[string]any{
		"validation_error": "bad schema",
		"rejected_output":  map[string]any{"foo": "bar"},
	})
	require.Contains(t, result, "bad schema")
	require.Contains(t, result, "foo")
	require.NotContains(t, result, "… (truncated,")
}

func TestBuildFeedbackPrefix_TruncatesLongOutput(t *testing.T) {
	longValue := strings.Repeat("x", 3000)
	result := pipeline.BuildFeedbackPrefix(map[string]any{
		"validation_error": "schema mismatch",
		"rejected_output":  map[string]any{"data": longValue},
	})
	require.Contains(t, result, "… (truncated,")
}
