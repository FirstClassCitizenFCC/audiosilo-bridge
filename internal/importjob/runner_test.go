package importjob

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kodestar/audiosilo-manager/internal/source"
	"github.com/kodestar/audiosilo-manager/internal/transfer"
)

func TestRunPlacesAndSkips(t *testing.T) {
	srcDir := t.TempDir()
	a := filepath.Join(srcDir, "A.m4b")
	b := filepath.Join(srcDir, "B.m4b")
	if err := os.WriteFile(a, []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	items := []PlannedItem{
		{Record: source.Record{SrcPath: a, Title: "A"}, RelPath: "Auth/A/A.m4b"},
		{Record: source.Record{SrcPath: b, Title: "B"}, RelPath: "Auth/B/B.m4b", ExistsOnServer: true},
	}

	var statuses []string
	res, err := Run(context.Background(), items, RunOptions{
		// Client nil → skip the rescan (no server in this unit test).
		Transfer: transfer.NewLocal(), TransferRoot: root,
		OnProgress: func(p Progress) { statuses = append(statuses, p.Status) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].Placed || !res[1].Skipped {
		t.Fatalf("results = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "Auth", "A", "A.m4b")); err != nil {
		t.Fatal("A should have been placed on disk")
	}
	if _, err := os.Stat(filepath.Join(root, "Auth", "B", "B.m4b")); !os.IsNotExist(err) {
		t.Fatal("B should have been skipped (exists on server)")
	}
	if !slices.Contains(statuses, "placed") || !slices.Contains(statuses, "skipped") {
		t.Fatalf("progress statuses = %v, want placed + skipped", statuses)
	}
}
