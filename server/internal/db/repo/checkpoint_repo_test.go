package repo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestCheckpointRepo(t *testing.T) {
	client := openDB(t)
	r := repo.NewCheckpointRepo(client)
	ctx := context.Background()

	cp, err := r.Create(ctx, repo.CreateCheckpointInput{
		TaskID: "task-1", Seq: 1, CommitSHA: "abc", TreeSHA: "def", FilesChanged: 3,
	})
	if err != nil || cp == nil {
		t.Fatal("Create failed:", err)
	}
	list, err := r.ListByTask(ctx, "task-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByTask: got %d, err %v", len(list), err)
	}
	latest, err := r.GetLatestByTask(ctx, "task-1")
	if err != nil || latest == nil || latest.Seq != 1 {
		t.Fatalf("GetLatestByTask: %v, %v", latest, err)
	}
	if err := r.DeleteByTask(ctx, "task-1"); err != nil {
		t.Fatal("DeleteByTask:", err)
	}
	list2, _ := r.ListByTask(ctx, "task-1")
	if len(list2) != 0 {
		t.Fatal("expected 0 after delete")
	}
}

func TestCheckpointRepo_Prune(t *testing.T) {
	client := openDB(t)
	r := repo.NewCheckpointRepo(client)
	ctx := context.Background()
	for i := 1; i <= 55; i++ {
		_, _ = r.Create(ctx, repo.CreateCheckpointInput{
			TaskID: "task-2", Seq: i, CommitSHA: fmt.Sprintf("sha%d", i), TreeSHA: "tree",
		})
	}
	prunedSeqs, err := r.PruneOldest(ctx, "task-2", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(prunedSeqs) != 5 {
		t.Fatalf("expected 5 pruned seqs, got %d: %v", len(prunedSeqs), prunedSeqs)
	}
	// The five oldest seqs (1..5) must be the ones reported pruned.
	for i, seq := range prunedSeqs {
		if seq != i+1 {
			t.Fatalf("prunedSeqs[%d]=%d, want %d", i, seq, i+1)
		}
	}
	list, _ := r.ListByTask(ctx, "task-2")
	if len(list) != 50 {
		t.Fatalf("after prune want 50, got %d", len(list))
	}
	// list is newest-first; the oldest remaining must be seq 6.
	if list[len(list)-1].Seq != 6 {
		t.Fatalf("expected oldest seq=6, got %d", list[len(list)-1].Seq)
	}
}
