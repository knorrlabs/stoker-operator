package syncengine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilesEqual_IdenticalFiles(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")
	writeTestFile(t, a, "same")
	writeTestFile(t, b, "same")

	if !filesEqual(a, b) {
		t.Error("identical files should be equal")
	}
}

func TestFilesEqual_DifferentFiles(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.txt")
	b := filepath.Join(tmp, "b.txt")
	writeTestFile(t, a, "aaa")
	writeTestFile(t, b, "bbb")

	if filesEqual(a, b) {
		t.Error("different files should not be equal")
	}
}

func TestFilesEqual_SymlinkA(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real.txt")
	link := filepath.Join(tmp, "link.txt")
	writeTestFile(t, real, "content")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	// Symlink as first arg — should return false even though content matches.
	if filesEqual(link, real) {
		t.Error("filesEqual should return false when first arg is a symlink")
	}
}

func TestFilesEqual_SymlinkB(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real.txt")
	link := filepath.Join(tmp, "link.txt")
	writeTestFile(t, real, "content")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	// Symlink as second arg — should return false.
	if filesEqual(real, link) {
		t.Error("filesEqual should return false when second arg is a symlink")
	}
}

func TestFilesEqual_BothSymlinks(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real.txt")
	linkA := filepath.Join(tmp, "linkA.txt")
	linkB := filepath.Join(tmp, "linkB.txt")
	writeTestFile(t, real, "content")
	if err := os.Symlink(real, linkA); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	if err := os.Symlink(real, linkB); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	if filesEqual(linkA, linkB) {
		t.Error("filesEqual should return false when both args are symlinks")
	}
}

func TestCopyFile_RejectsSymlinkSource(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "credential.txt")
	link := filepath.Join(tmp, "config.txt")
	dst := filepath.Join(tmp, "dest", "config.txt")
	writeTestFile(t, target, "secret")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	wrote, err := copyFile(link, dst)
	if err == nil {
		t.Fatal("copyFile should reject a symlink source, got nil error")
	}
	if wrote {
		t.Error("copyFile should not report write when rejecting symlink")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("destination must not be created when source is symlink (stat err: %v)", statErr)
	}
}

func TestFilesEqual_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "exists.txt")
	writeTestFile(t, a, "x")

	if filesEqual(a, filepath.Join(tmp, "missing.txt")) {
		t.Error("filesEqual should return false when a file is missing")
	}
}
