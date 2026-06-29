package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSafeJoinAllowed(t *testing.T) {
	root := t.TempDir()
	got, err := SafeJoin(root, "Author/Book/file.m4b")
	if err != nil {
		t.Fatalf("allowed path rejected: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("joined %q not under root %q", got, root)
	}
}

// SafeJoin's security contract is that traversal cannot escape the root: it
// neutralizes ".." / absolute prefixes by clamping the result inside root (it does
// not necessarily error). Either erroring or clamping is acceptable; escaping is not.
func TestSafeJoinNeutralizesTraversal(t *testing.T) {
	root := t.TempDir()
	rootAbs, _ := filepath.Abs(root)
	for _, rel := range []string{"../escape", "../../etc/passwd", "a/../../b", "/abs/path", "../../../"} {
		got, err := SafeJoin(root, rel)
		if err != nil {
			continue // rejecting outright is also acceptable
		}
		if got != rootAbs && !strings.HasPrefix(got, rootAbs+string(filepath.Separator)) {
			t.Errorf("SafeJoin(%q) = %q escapes root %q", rel, got, rootAbs)
		}
	}
}

func TestLocalPlaceAndIdempotency(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "Soulsmith.m4b")
	writeFile(t, src, "hello-audio")
	root := t.TempDir()
	l := NewLocal()
	req := PlaceRequest{Root: root, RelDir: "Will Wight/Cradle/C02 - Soulsmith", Filename: "Soulsmith.m4b", SrcPath: src}

	res, err := l.Place(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.AlreadyPresent {
		t.Fatal("first place should not be AlreadyPresent")
	}
	if b, _ := os.ReadFile(res.DestPath); string(b) != "hello-audio" {
		t.Fatalf("content = %q", b)
	}

	res2, err := l.Place(ctx, req) // re-place same source
	if err != nil {
		t.Fatal(err)
	}
	if !res2.AlreadyPresent {
		t.Fatal("re-place of the same file should be AlreadyPresent (idempotent)")
	}
}

func TestLocalPlaceSizeMismatchRefused(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dst := filepath.Join(root, "B", "f.m4b")
	writeFile(t, dst, "old-bigger-content")
	src := filepath.Join(t.TempDir(), "f.m4b")
	writeFile(t, src, "new")
	l := NewLocal()

	if _, err := l.Place(ctx, PlaceRequest{Root: root, RelDir: "B", Filename: "f.m4b", SrcPath: src}); err == nil {
		t.Fatal("expected refusal on size mismatch without Overwrite")
	}
	if _, err := l.Place(ctx, PlaceRequest{Root: root, RelDir: "B", Filename: "f.m4b", SrcPath: src, Overwrite: true}); err != nil {
		t.Fatalf("overwrite failed: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("overwrite content = %q", b)
	}
}

func TestLocalProgressAndContextCancel(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.m4b")
	writeFile(t, src, strings.Repeat("x", 4096))
	root := t.TempDir()

	var lastCopied, total int64
	_, err := NewLocal().Place(context.Background(), PlaceRequest{
		Root: root, RelDir: "d", Filename: "a.m4b", SrcPath: src,
		Progress: func(c, tot int64) { lastCopied, total = c, tot },
	})
	if err != nil {
		t.Fatal(err)
	}
	if lastCopied != 4096 || total != 4096 {
		t.Fatalf("progress = %d/%d, want 4096/4096", lastCopied, total)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewLocal().Place(cancelled, PlaceRequest{Root: root, RelDir: "d2", Filename: "b.m4b", SrcPath: src}); err == nil {
		t.Fatal("expected error for a cancelled context")
	}
	if _, err := os.Stat(filepath.Join(root, "d2", "b.m4b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a cancelled place must not leave a destination file")
	}
}

func TestLocalIndexFindAcrossRename(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Author", "Old Folder", "Book.m4b"), "content-123")
	writeFile(t, filepath.Join(root, "Author", "Old Folder", "cover.jpg"), "img") // non-audio: ignored

	idx, err := NewLocal().Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Count() != 1 {
		t.Fatalf("index count = %d, want 1 (audio only)", idx.Count())
	}
	rel, found, err := idx.Find("Book.m4b", int64(len("content-123")))
	if err != nil {
		t.Fatal(err)
	}
	if !found || filepath.Base(rel) != "Book.m4b" {
		t.Fatalf("Find = %q, %v (should match across the folder rename)", rel, found)
	}
	if _, found, _ := idx.Find("Book.m4b", 999); found {
		t.Fatal("a different size should not match")
	}
}

func TestEnsureRootMissing(t *testing.T) {
	if err := NewLocal().EnsureRoot(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for a missing root")
	}
}
