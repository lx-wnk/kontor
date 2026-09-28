package db_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
)

func TestMarkerStore_RecordsOnce(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()
	ctx := t.Context()
	m := db.MarkerStore{DB: bundle.DB}

	has, err := m.Has(ctx, "probe")
	require.NoError(t, err)
	require.False(t, has)

	require.NoError(t, m.Record(ctx, "probe"))
	require.NoError(t, m.Record(ctx, "probe"), "recording twice is not an error")

	has, err = m.Has(ctx, "probe")
	require.NoError(t, err)
	require.True(t, has)
}
