package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	transportclient "github.com/go-git/go-git/v5/plumbing/transport/client"
)

const gitRemoteOrigin = "origin"

// Result holds the outcome of a clone or fetch operation.
type Result struct {
	Commit string
	Ref    string
}

// Client is the interface for git operations.
type Client interface {
	// LsRemote resolves a ref to a commit SHA via a single HTTP/SSH call
	// without cloning the repository. Used by the controller.
	LsRemote(ctx context.Context, repoURL, ref string, auth transport.AuthMethod) (Result, error)

	// CloneOrFetch clones the repo if the target directory is empty,
	// or fetches + checks out the ref if already cloned.
	// Used by the agent sidecar.
	CloneOrFetch(ctx context.Context, repoURL, ref, path string, auth transport.AuthMethod) (Result, error)
}

// GoGitClient implements Client using go-git.
type GoGitClient struct{}

var _ Client = (*GoGitClient)(nil)

func (g *GoGitClient) LsRemote(ctx context.Context, repoURL, ref string, auth transport.AuthMethod) (Result, error) {
	ep, err := transport.NewEndpoint(repoURL)
	if err != nil {
		return Result{}, fmt.Errorf("parsing endpoint %s: %w", repoURL, err)
	}

	cli, err := transportclient.NewClient(ep)
	if err != nil {
		return Result{}, fmt.Errorf("creating transport for %s: %w", repoURL, err)
	}

	sess, err := cli.NewUploadPackSession(ep, auth)
	if err != nil {
		return Result{}, fmt.Errorf("opening session for %s: %w", repoURL, err)
	}
	defer func() { _ = sess.Close() }()

	ar, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("ls-remote %s: %w", repoURL, err)
	}

	return matchRef(ar, ref, repoURL)
}

// matchRef resolves a ref string against AdvertisedReferences.
// For annotated tags, go-git's AdvRefs.Peeled map contains the actual commit
// hash (what native git shows as refs/tags/X^{}). We check Peeled first to
// avoid returning the tag object hash, which would cause the agent (which uses
// rev-parse) to disagree and re-sync every cycle.
func matchRef(ar *packp.AdvRefs, ref, repoURL string) (Result, error) {
	// If ref is already a full SHA, return it directly
	if plumbing.IsHash(ref) {
		return Result{Commit: ref, Ref: ref}, nil
	}

	// Search for matching ref: exact tag, then branch
	candidates := []string{
		"refs/tags/" + ref,
		"refs/heads/" + ref,
	}

	// Check peeled refs first (annotated tags). The Peeled map keys are the
	// ref names (e.g. "refs/tags/2.2.3") and values are the dereferenced
	// commit hashes — exactly what the agent resolves via rev-parse.
	for _, candidate := range candidates {
		if hash, ok := ar.Peeled[candidate]; ok {
			return Result{Commit: hash.String(), Ref: ref}, nil
		}
	}

	// Fall back to non-peeled refs (lightweight tags, branches)
	for _, candidate := range candidates {
		if hash, ok := ar.References[candidate]; ok {
			return Result{Commit: hash.String(), Ref: ref}, nil
		}
	}

	return Result{}, fmt.Errorf("ref %q not found in remote %s", ref, repoURL)
}

func (g *GoGitClient) CloneOrFetch(ctx context.Context, repoURL, ref, path string, auth transport.AuthMethod) (Result, error) {
	// Check if the directory already contains a cloned repo
	if isCloned(path) {
		return g.fetchAndCheckout(ctx, repoURL, ref, path, auth)
	}
	return g.cloneAndCheckout(ctx, repoURL, ref, path, auth)
}

func (g *GoGitClient) cloneAndCheckout(ctx context.Context, repoURL, ref, path string, auth transport.AuthMethod) (Result, error) {
	repo, err := gogit.PlainCloneContext(ctx, path, false, &gogit.CloneOptions{
		URL:   repoURL,
		Auth:  auth,
		Depth: 1,
	})
	if err != nil {
		return Result{}, fmt.Errorf("git clone %s: %w", repoURL, err)
	}

	return checkoutRef(repo, ref)
}

func (g *GoGitClient) fetchAndCheckout(ctx context.Context, repoURL, ref, path string, auth transport.AuthMethod) (Result, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return Result{}, fmt.Errorf("opening repo at %s: %w", path, err)
	}

	// Ensure the remote URL matches the CR spec (handles repo URL changes).
	if err := ensureRemoteURL(repo, repoURL); err != nil {
		return Result{}, err
	}

	err = repo.FetchContext(ctx, &gogit.FetchOptions{
		Auth:  auth,
		Force: true,
		Tags:  gogit.AllTags,
	})
	if err != nil && err != gogit.NoErrAlreadyUpToDate {
		return Result{}, fmt.Errorf("git fetch: %w", err)
	}

	return checkoutRef(repo, ref)
}

// ensureRemoteURL updates the origin remote URL if it differs from the desired URL.
func ensureRemoteURL(repo *gogit.Repository, desiredURL string) error {
	remote, err := repo.Remote(gitRemoteOrigin)
	if err != nil {
		return fmt.Errorf("getting origin remote: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) > 0 && urls[0] == desiredURL {
		return nil
	}
	// Remove and re-add origin with the correct URL
	if err := repo.DeleteRemote(gitRemoteOrigin); err != nil {
		return fmt.Errorf("deleting origin remote: %w", err)
	}
	if _, err := repo.CreateRemote(&gogitconfig.RemoteConfig{
		Name: gitRemoteOrigin,
		URLs: []string{desiredURL},
	}); err != nil {
		return fmt.Errorf("creating origin remote: %w", err)
	}
	return nil
}

// checkoutRef resolves a ref (branch, tag, or commit SHA) and checks it out.
func checkoutRef(repo *gogit.Repository, ref string) (Result, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return Result{}, fmt.Errorf("getting worktree: %w", err)
	}

	hash, err := resolveRef(repo, ref)
	if err != nil {
		return Result{}, err
	}

	if err := wt.Checkout(&gogit.CheckoutOptions{
		Hash:  hash,
		Force: true,
	}); err != nil {
		return Result{}, fmt.Errorf("checkout %s: %w", ref, err)
	}

	return Result{
		Commit: hash.String(),
		Ref:    ref,
	}, nil
}

// resolveRef tries to resolve a ref as: exact commit SHA, tag, then branch.
func resolveRef(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	// Try as a full SHA
	if plumbing.IsHash(ref) {
		return plumbing.NewHash(ref), nil
	}

	// Try as a tag
	tagRef, err := repo.Tag(ref)
	if err == nil {
		return tagRef.Hash(), nil
	}

	// Try as refs/tags/
	resolved, err := repo.ResolveRevision(plumbing.Revision("refs/tags/" + ref))
	if err == nil {
		return *resolved, nil
	}

	// Try as a branch (remote tracking)
	resolved, err = repo.ResolveRevision(plumbing.Revision("refs/remotes/origin/" + ref))
	if err == nil {
		return *resolved, nil
	}

	// Last resort: let go-git try to resolve it
	resolved, err = repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("cannot resolve ref %q: %w", ref, err)
	}
	return *resolved, nil
}

// isCloned checks if a directory contains a valid git repository.
func isCloned(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// NativeGitClient implements Client using the native git binary via exec.Command.
// Unlike GoGitClient, it streams pack data rather than loading it into memory,
// making it suitable for large repositories where go-git causes OOM kills.
type NativeGitClient struct{}

var _ Client = (*NativeGitClient)(nil)

// LsRemote is not supported by NativeGitClient. The controller uses GoGitClient for this.
func (g *NativeGitClient) LsRemote(_ context.Context, _, _ string, _ transport.AuthMethod) (Result, error) {
	return Result{}, fmt.Errorf("NativeGitClient does not support LsRemote")
}

// CloneOrFetch clones or fetches using the native git binary.
// The transport.AuthMethod parameter is ignored; auth is configured via
// GIT_SSH_KEY_FILE (SSH key path) or GIT_TOKEN_FILE (token path) env vars.
func (g *NativeGitClient) CloneOrFetch(ctx context.Context, repoURL, ref, path string, _ transport.AuthMethod) (Result, error) {
	authURL, env, cleanup, err := buildGitEnv(repoURL)
	if err != nil {
		return Result{}, fmt.Errorf("setting up git env: %w", err)
	}
	defer cleanup()

	if isCloned(path) {
		return nativeFetchAndCheckout(ctx, repoURL, authURL, ref, path, env)
	}
	return nativeCloneAndCheckout(ctx, repoURL, authURL, ref, path, env)
}

// buildGitEnv prepares environment variables for native git commands.
// For SSH repos, copies the key to /tmp with 0600 permissions and sets GIT_SSH_COMMAND.
// For token repos, injects the token into the URL.
// Returns the (possibly modified) URL, env vars, a cleanup func, and any error.
func buildGitEnv(repoURL string) (string, []string, func(), error) {
	base := []string{
		"HOME=/tmp",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	noop := func() {}

	// Write a gitconfig to $HOME (/tmp) marking all directories as safe.
	// Required when the container runs as a non-root UID that doesn't own the
	// emptyDir mount point (git 2.35.2+ safe.directory ownership check).
	_ = os.WriteFile("/tmp/.gitconfig", []byte("[safe]\n\tdirectory = *\n"), 0644)

	if keyFile := os.Getenv("GIT_SSH_KEY_FILE"); keyFile != "" {
		keyData, err := os.ReadFile(keyFile)
		if err != nil {
			return repoURL, nil, noop, fmt.Errorf("reading SSH key %s: %w", keyFile, err)
		}
		tmpKey := "/tmp/stoker-ssh-key"
		if err := os.WriteFile(tmpKey, keyData, 0600); err != nil {
			return repoURL, nil, noop, fmt.Errorf("writing SSH key to /tmp: %w", err)
		}

		// Determine host key checking mode based on GIT_KNOWN_HOSTS_FILE.
		hostKeyOpts := "-o StrictHostKeyChecking=no"
		if knownHostsFile := os.Getenv("GIT_KNOWN_HOSTS_FILE"); knownHostsFile != "" {
			hostKeyOpts = fmt.Sprintf("-o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s", knownHostsFile)
		}

		// OpenSSH refuses to run when the current UID has no /etc/passwd entry.
		// Kubernetes pods inherit runAsUser from the pod spec (e.g. uid 2003 for
		// Ignition), which may not exist in Alpine's passwd. Write a shell wrapper
		// that uses nss_wrapper to inject a minimal passwd entry for the current UID
		// before invoking ssh. The wrapper lives in /tmp (writable emptyDir).
		wrapperPath := "/tmp/stoker-ssh"
		wrapper := fmt.Sprintf(`#!/bin/sh
uid=$(id -u); gid=$(id -g)
printf "agent:x:%%d:%%d::/tmp:/sbin/nologin\n" "$uid" "$gid" > /tmp/.nss-passwd
printf "agent:x:%%d:\n" "$gid" > /tmp/.nss-group
_nss=$(ls /usr/lib/libnss_wrapper.so* 2>/dev/null | head -1)
if [ -n "$_nss" ]; then
  NSS_WRAPPER_PASSWD=/tmp/.nss-passwd NSS_WRAPPER_GROUP=/tmp/.nss-group LD_PRELOAD="$_nss" \
  exec ssh -i %s %s -o BatchMode=yes -o IdentitiesOnly=yes "$@"
else
  exec ssh -i %s %s -o BatchMode=yes -o IdentitiesOnly=yes "$@"
fi
`, tmpKey, hostKeyOpts, tmpKey, hostKeyOpts)
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0700); err != nil {
			_ = os.Remove(tmpKey)
			return repoURL, nil, noop, fmt.Errorf("writing SSH wrapper: %w", err)
		}
		env := append(base, "GIT_SSH_COMMAND="+wrapperPath)
		cleanup := func() {
			_ = os.Remove(tmpKey)
			_ = os.Remove(wrapperPath)
		}
		return repoURL, env, cleanup, nil
	}

	if tokenFile := os.Getenv("GIT_TOKEN_FILE"); tokenFile != "" {
		tokenData, err := os.ReadFile(tokenFile)
		if err != nil {
			return repoURL, nil, noop, fmt.Errorf("reading token file %s: %w", tokenFile, err)
		}
		token := strings.TrimSpace(string(tokenData))
		return injectTokenIntoURL(repoURL, token), base, noop, nil
	}

	return repoURL, base, noop, nil
}

// injectTokenIntoURL inserts an OAuth token credential into an HTTPS git URL.
func injectTokenIntoURL(repoURL, token string) string {
	if after, ok := strings.CutPrefix(repoURL, "https://"); ok {
		return "https://x-access-token:" + token + "@" + after
	}
	if after, ok := strings.CutPrefix(repoURL, "http://"); ok {
		return "http://x-access-token:" + token + "@" + after
	}
	return repoURL
}

// nativeCloneAndCheckout performs the initial clone. cleanURL is what gets
// persisted as the origin remote; authURL (which may embed a token) is only
// ever passed on the command line so credentials never land in .git/config.
func nativeCloneAndCheckout(ctx context.Context, cleanURL, authURL, ref, path string, env []string) (Result, error) {
	// `git clone --branch` only accepts branch and tag names. For a commit
	// SHA, init an empty repo and reuse the fetch path, which fetches the
	// SHA directly (supported by GitHub/GitLab via allow-reachable-SHA1).
	if plumbing.IsHash(ref) {
		if _, err := runGit(ctx, []string{"init", path}, "", env); err != nil {
			return Result{}, fmt.Errorf("git init: %w", err)
		}
		if _, err := runGit(ctx, []string{"remote", "add", gitRemoteOrigin, cleanURL}, path, env); err != nil {
			return Result{}, fmt.Errorf("git remote add: %w", err)
		}
		return nativeFetchAndCheckout(ctx, cleanURL, authURL, ref, path, env)
	}

	if _, err := runGit(ctx, []string{"clone", "--depth=1", "--branch", ref, authURL, path}, "", env); err != nil {
		return Result{}, fmt.Errorf("git clone --branch %s: %w", ref, err)
	}
	// Replace the persisted origin URL with the credential-free form.
	if _, err := runGit(ctx, []string{"remote", "set-url", gitRemoteOrigin, cleanURL}, path, env); err != nil {
		return Result{}, fmt.Errorf("git remote set-url: %w", err)
	}
	return nativeRevParse(ctx, ref, path, env)
}

func nativeFetchAndCheckout(ctx context.Context, cleanURL, authURL, ref, path string, env []string) (Result, error) {
	// Keep the persisted remote URL current with the CR spec (credential-free);
	// the fetch itself uses the auth URL directly so the token stays off disk.
	if _, err := runGit(ctx, []string{"remote", "set-url", gitRemoteOrigin, cleanURL}, path, env); err != nil {
		return Result{}, fmt.Errorf("git remote set-url: %w", err)
	}
	if _, err := runGit(ctx, []string{"fetch", "--depth=1", authURL, ref}, path, env); err != nil {
		return Result{}, fmt.Errorf("git fetch: %w", err)
	}
	if _, err := runGit(ctx, []string{"checkout", "-f", "FETCH_HEAD"}, path, env); err != nil {
		return Result{}, fmt.Errorf("git checkout: %w", err)
	}
	return nativeRevParse(ctx, ref, path, env)
}

func nativeRevParse(ctx context.Context, ref, path string, env []string) (Result, error) {
	commit, err := runGit(ctx, []string{"rev-parse", "HEAD"}, path, env)
	if err != nil {
		return Result{}, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return Result{Commit: commit, Ref: ref}, nil
}

// runGit runs a git command and returns the trimmed combined output.
func runGit(ctx context.Context, args []string, dir string, extraEnv []string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := sanitizeOutput(string(out))
		if msg == "" {
			// No command output (e.g. binary missing, signal kill) — surface
			// the exec error itself rather than an empty message.
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// tokenRe matches credential tokens embedded in git URLs (https://user:token@host).
var tokenRe = regexp.MustCompile(`://[^@\s]+@`)

// sanitizeOutput strips credential tokens from git output before logging or surfacing in status.
func sanitizeOutput(s string) string {
	return tokenRe.ReplaceAllString(strings.TrimSpace(s), "://<redacted>@")
}
