package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initFixtureRepo creates a local git repo with two commits and returns the
// repo path plus both commit SHAs (first, second). allowReachableSHA1InWant is
// enabled so SHA fetches over the file/local transport behave like GitHub.
func initFixtureRepo(t *testing.T) (repoPath, firstSHA, secondSHA string) {
	t.Helper()
	repoPath = filepath.Join(t.TempDir(), "fixture")

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	run("init", "--initial-branch=main")
	run("config", "uploadpack.allowReachableSHA1InWant", "true")

	if err := os.WriteFile(filepath.Join(repoPath, "a.txt"), []byte("one"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "first")
	firstSHA = run("rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repoPath, "a.txt"), []byte("two"), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "second")
	secondSHA = run("rev-parse", "HEAD")

	return repoPath, firstSHA, secondSHA
}

// TestNativeCloneCommitSHA covers the CRD's promise that spec.git.ref may be a
// commit SHA: `git clone --branch <sha>` is invalid, so the initial clone must
// take the init+fetch path instead of failing permanently.
func TestNativeCloneCommitSHA(t *testing.T) {
	t.Setenv("GIT_SSH_KEY_FILE", "")
	t.Setenv("GIT_TOKEN_FILE", "")

	repoPath, firstSHA, _ := initFixtureRepo(t)
	dst := filepath.Join(t.TempDir(), "clone")

	client := &NativeGitClient{}
	result, err := client.CloneOrFetch(context.Background(), repoPath, firstSHA, dst, nil)
	if err != nil {
		t.Fatalf("CloneOrFetch with SHA ref: %v", err)
	}
	if result.Commit != firstSHA {
		t.Errorf("checked-out commit = %s, want %s", result.Commit, firstSHA)
	}

	content, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatalf("reading synced file: %v", err)
	}
	if string(content) != "one" {
		t.Errorf("a.txt = %q, want %q (content of first commit)", content, "one")
	}
}

// TestNativeFetchCommitSHA covers moving an existing clone to a different
// pinned SHA, the path a Kargo promotion to a commit ref exercises.
func TestNativeFetchCommitSHA(t *testing.T) {
	t.Setenv("GIT_SSH_KEY_FILE", "")
	t.Setenv("GIT_TOKEN_FILE", "")

	repoPath, firstSHA, secondSHA := initFixtureRepo(t)
	dst := filepath.Join(t.TempDir(), "clone")
	client := &NativeGitClient{}
	ctx := context.Background()

	if _, err := client.CloneOrFetch(ctx, repoPath, "main", dst, nil); err != nil {
		t.Fatalf("initial clone: %v", err)
	}

	result, err := client.CloneOrFetch(ctx, repoPath, firstSHA, dst, nil)
	if err != nil {
		t.Fatalf("fetch to first SHA: %v", err)
	}
	if result.Commit != firstSHA {
		t.Errorf("commit after pin = %s, want %s", result.Commit, firstSHA)
	}

	result, err = client.CloneOrFetch(ctx, repoPath, secondSHA, dst, nil)
	if err != nil {
		t.Fatalf("fetch to second SHA: %v", err)
	}
	if result.Commit != secondSHA {
		t.Errorf("commit after re-pin = %s, want %s", result.Commit, secondSHA)
	}
}

// TestNativeCloneBranch pins the pre-existing branch clone path so the SHA
// special-case doesn't regress it.
func TestNativeCloneBranch(t *testing.T) {
	t.Setenv("GIT_SSH_KEY_FILE", "")
	t.Setenv("GIT_TOKEN_FILE", "")

	repoPath, _, secondSHA := initFixtureRepo(t)
	dst := filepath.Join(t.TempDir(), "clone")

	client := &NativeGitClient{}
	result, err := client.CloneOrFetch(context.Background(), repoPath, "main", dst, nil)
	if err != nil {
		t.Fatalf("CloneOrFetch with branch ref: %v", err)
	}
	if result.Commit != secondSHA {
		t.Errorf("checked-out commit = %s, want branch head %s", result.Commit, secondSHA)
	}
}

// TestAuthURLStaysOutOfGitConfig guards the credential-handling contract:
// the token-bearing URL form is only ever passed on the git command line,
// while .git/config persists the credential-free URL. A persisted auth URL
// would keep the token on disk for the lifetime of the repo volume. The
// fixture path stands in for the auth URL; the clean URL is a sentinel that
// is never contacted, so finding it (and only it) in config proves the
// set-url/fetch split.
func TestAuthURLStaysOutOfGitConfig(t *testing.T) {
	repoPath, firstSHA, _ := initFixtureRepo(t)
	const cleanURL = "https://example.invalid/repo.git"
	dst := filepath.Join(t.TempDir(), "clone")
	ctx := context.Background()

	if _, err := nativeCloneAndCheckout(ctx, cleanURL, repoPath, "main", dst, nil); err != nil {
		t.Fatalf("clone: %v", err)
	}
	assertOriginURL(t, dst, cleanURL, repoPath)

	if _, err := nativeFetchAndCheckout(ctx, cleanURL, repoPath, firstSHA, dst, nil); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	assertOriginURL(t, dst, cleanURL, repoPath)
}

func assertOriginURL(t *testing.T, repoDir, want, authURL string) {
	t.Helper()
	cfg, err := os.ReadFile(filepath.Join(repoDir, ".git", "config"))
	if err != nil {
		t.Fatalf("reading .git/config: %v", err)
	}
	if !strings.Contains(string(cfg), want) {
		t.Errorf("origin URL not set to clean URL %q:\n%s", want, cfg)
	}
	if strings.Contains(string(cfg), authURL) {
		t.Errorf("auth URL %q persisted in .git/config:\n%s", authURL, cfg)
	}
}
