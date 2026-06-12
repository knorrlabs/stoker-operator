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

// TestCopyFile_AtomicWriteLeavesNoTempFiles guards the temp-file-plus-rename
// contract: the destination must end up with the source's content and mode,
// and no scratch files may linger for the gateway's scan to pick up.
func TestCopyFile_AtomicWriteLeavesNoTempFiles(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.json")
	dst := filepath.Join(tmp, "dest", "config.json")
	writeTestFile(t, src, `{"a":1}`)
	if err := os.Chmod(src, 0755); err != nil {
		t.Fatalf("chmod src: %v", err)
	}

	wrote, err := copyFile(src, dst)
	if err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if !wrote {
		t.Error("copyFile should report a write for a new destination")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("dst content = %q, want %q", got, `{"a":1}`)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("dst mode = %v, want 0755", info.Mode().Perm())
	}

	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatalf("reading dst dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("temp files left behind in destination dir: %v", names)
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
