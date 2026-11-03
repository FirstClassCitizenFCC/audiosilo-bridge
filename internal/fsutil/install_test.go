package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestInstallNoReplace(t *testing.T) {
	unsupported := func(_ *os.Root, oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: UnsupportedLinkErrors[0]}
	}
	for name, link := range map[string]LinkFunc{"hard link": nil, "rename fallback": unsupported} {
		t.Run(name, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			if err := root.WriteFile("staged", []byte("new"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := root.WriteFile("target", []byte("existing"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := InstallNoReplace(root, "staged", "target", link); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("replaced an existing target: %v", err)
			}
			if got, _ := root.ReadFile("target"); string(got) != "existing" {
				t.Fatal("target overwritten")
			}
			linked, err := InstallNoReplace(root, "staged", "fresh", link)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := root.ReadFile("fresh"); string(got) != "new" {
				t.Fatal("installed bytes differ")
			}
			_, serr := root.Lstat("staged")
			if linked != (serr == nil) {
				t.Fatalf("linked=%v but staged stat err=%v", linked, serr)
			}
		})
	}
}

func TestFreeBytes(t *testing.T) {
	n, err := FreeBytes(t.TempDir())
	if err != nil || n <= 0 {
		t.Fatalf("FreeBytes = %d, %v", n, err)
	}
	if _, err := FreeBytes("/no/such/dir/at/all"); err == nil {
		t.Fatal("a missing path has no free space to read")
	}
	if IsNoSpace(os.ErrPermission) {
		t.Fatal("permission is not no-space")
	}
}

func TestNoReplaceRenameRefusesExistingTarget(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for name, rename := range map[string]func(*os.Root, string, string) error{"platform": RenameNoReplace, "portable": RenameIfAbsent} {
		t.Run(name, func(t *testing.T) {
			if err := root.WriteFile("a.mp3", []byte("staged"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := root.WriteFile("b.mp3", []byte("existing"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := rename(root, "a.mp3", "b.mp3"); !errors.Is(err, ErrExists) {
				t.Fatal("replaced an existing target:", err)
			}
			if got, _ := root.ReadFile("b.mp3"); string(got) != "existing" {
				t.Fatal("target overwritten")
			}
			if err := root.Remove("b.mp3"); err != nil {
				t.Fatal(err)
			}
			if err := rename(root, "a.mp3", "b.mp3"); err != nil {
				t.Fatal("absent target refused:", err)
			}
			if got, _ := root.ReadFile("b.mp3"); string(got) != "staged" {
				t.Fatal("rename lost bytes")
			}
			if _, err := root.Lstat("a.mp3"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("source kept", err)
			}
			_ = root.Remove("b.mp3")
		})
	}
}
