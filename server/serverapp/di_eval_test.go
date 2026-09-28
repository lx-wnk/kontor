package serverapp

import (
	"bytes"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/eval"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

func TestEvalOnDrift_BroadcastsEvalDriftEvent(t *testing.T) {
	b := sse.NewBroadcaster()
	tb := sse.NewTaskBroadcaster(b)
	ch := tb.Subscribe()
	defer tb.Unsubscribe(ch)

	fn := evalOnDrift(tb)
	fn([]eval.DriftFinding{{
		Dim:       eval.Dimension{Stage: "implementation"},
		MetricKey: eval.MetricSuccessRate,
		Direction: eval.DirectionDown,
	}})

	select {
	case frame := <-ch:
		if !bytes.Contains(frame, []byte("eval_drift")) {
			t.Errorf("want eval_drift in SSE frame, got: %s", frame)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out: no SSE frame received")
	}
}

func TestEvalOnDrift_MultipleFindings_SingleBroadcast(t *testing.T) {
	b := sse.NewBroadcaster()
	tb := sse.NewTaskBroadcaster(b)
	ch := tb.Subscribe()
	defer tb.Unsubscribe(ch)

	fn := evalOnDrift(tb)
	fn([]eval.DriftFinding{
		{Dim: eval.Dimension{Stage: "implementation"}, MetricKey: eval.MetricSuccessRate},
		{Dim: eval.Dimension{Stage: "self_review"}, MetricKey: eval.MetricMeanIterations},
	})

	select {
	case frame := <-ch:
		if !bytes.Contains(frame, []byte("eval_drift")) {
			t.Errorf("want eval_drift frame, got: %s", frame)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out: no SSE frame received")
	}
	// Exactly one frame (one broadcast per onDrift call, not per finding).
	select {
	case unexpected := <-ch:
		t.Errorf("want exactly 1 frame, got extra: %s", unexpected)
	case <-time.After(10 * time.Millisecond):
		// correct
	}
}
