package pipeline_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func TestNextStage_PlanReview(t *testing.T) {
	require.Equal(t, "plan_review", pipeline.NextStage("ready"))
	require.Equal(t, "implementation", pipeline.NextStage("plan_review"))
}
