package syncengine

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyFile copies src to dst, creating parent directories as needed.
// Returns true if the file was actually written (new or changed).
// Symlinks at src are rejected: a malicious repo could symlink to credential
// mounts or other host paths and have them copied into the destination.
func copyFile(src, dst string) (bool, error) {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return false, fmt.Errorf("stat source %s: %w", src, err)
	}
	if srcInfo.Mode()&fs.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing to copy symlink source: %s", src)
	}

	// Fast path: if dst exists with same size, compare hashes.
	if filesEqual(src, dst) {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return false, fmt.Errorf("creating parent dir for %s: %w", dst, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return false, fmt.Errorf("opening source %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return false, fmt.Errorf("creating destination %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return false, fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}

	_ = os.Chmod(dst, srcInfo.Mode())

	return true, nil
}

// filesEqual returns true if both files exist and have identical content.
// Returns false if either path is a symlink.
func filesEqual(a, b string) bool {
	infoA, errA := os.Lstat(a)
	infoB, errB := os.Lstat(b)
	if errA != nil || errB != nil {
		return false
	}
	// Never consider symlinks equal.
	if infoA.Mode()&fs.ModeSymlink != 0 || infoB.Mode()&fs.ModeSymlink != 0 {
		return false
	}
	// Quick size check.
	if infoA.Size() != infoB.Size() {
		return false
	}
	hashA, errA := sha256File(a)
	hashB, errB := sha256File(b)
	if errA != nil || errB != nil {
		return false
	}
	return hashA == hashB
}

// sha256File returns the hex-encoded SHA-256 hash of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
