package merger

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/sdk"
)

func TestChainEnrichers_NilWhenNoneActive(t *testing.T) {
	if ChainEnrichers() != nil || ChainEnrichers(nil, nil) != nil {
		t.Error("ChainEnrichers with no active enrichers should return nil")
	}
}

func TestChainEnrichers_AppliesAllInOrder(t *testing.T) {
	var order []string
	a := func(_ context.Context, _ []sdk.Agent) { order = append(order, "a") }
	b := func(_ context.Context, _ []sdk.Agent) { order = append(order, "b") }

	ChainEnrichers(a, nil, b)(context.Background(), nil)

	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("apply order = %v, want [a b]", order)
	}
}
