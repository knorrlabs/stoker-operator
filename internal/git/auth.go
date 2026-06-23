package git

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	stokerv1alpha1 "github.com/knorrlabs/stoker-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// ResolveAuth reads the appropriate Secret and builds a go-git transport.AuthMethod.
// Returns nil auth (valid for public repos) if no auth is configured.
func ResolveAuth(ctx context.Context, c client.Client, namespace string, authSpec *stokerv1alpha1.GitAuthSpec) (transport.AuthMethod, error) {
	if authSpec == nil {
		return nil, nil
	}

	switch {
	case authSpec.SSHKey != nil:
		return resolveSSHAuth(ctx, c, namespace, authSpec.SSHKey)
	case authSpec.Token != nil:
		return resolveTokenAuth(ctx, c, namespace, authSpec.Token)
	case authSpec.GitHubApp != nil:
		return resolveGitHubAppAuth(ctx, c, namespace, authSpec.GitHubApp)
	default:
		return nil, nil
	}
}

func resolveGitHubAppAuth(ctx context.Context, c client.Client, namespace string, appAuth *stokerv1alpha1.GitHubAppAuth) (transport.AuthMethod, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: appAuth.PrivateKeySecretRef.Name, Namespace: namespace}
	if err := c.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting GitHub App PEM secret %s/%s: %w", namespace, appAuth.PrivateKeySecretRef.Name, err)
	}

	pemBytes, ok := secret.Data[appAuth.PrivateKeySecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s/%s", appAuth.PrivateKeySecretRef.Key, namespace, appAuth.PrivateKeySecretRef.Name)
	}

	result, err := ExchangeGitHubAppToken(ctx, pemBytes, appAuth.AppID, appAuth.InstallationID, appAuth.APIBaseURL)
	if err != nil {
		return nil, fmt.Errorf("exchanging GitHub App token: %w", err)
	}

	return &gogithttp.BasicAuth{
		Username: "x-access-token",
		Password: result.Token,
	}, nil
}

func resolveSSHAuth(ctx context.Context, c client.Client, namespace string, sshAuth *stokerv1alpha1.SSHKeyAuth) (transport.AuthMethod, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: sshAuth.SecretRef.Name, Namespace: namespace}
	if err := c.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting SSH key secret %s/%s: %w", namespace, sshAuth.SecretRef.Name, err)
	}

	pemBytes, ok := secret.Data[sshAuth.SecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s/%s", sshAuth.SecretRef.Key, namespace, sshAuth.SecretRef.Name)
	}

	publicKey, err := gogitssh.NewPublicKeys("git", pemBytes, "")
	if err != nil {
		return nil, fmt.Errorf("parsing SSH private key: %w", err)
	}

	if sshAuth.KnownHosts != nil {
		hostKeyCallback, err := resolveKnownHosts(ctx, c, namespace, sshAuth.KnownHosts)
		if err != nil {
			return nil, err
		}
		publicKey.HostKeyCallback = hostKeyCallback
	} else {
		publicKey.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	return publicKey, nil
}

// resolveKnownHosts reads a known_hosts Secret and returns a HostKeyCallback.
// Uses a temp file because knownhosts.New() requires a file path.
func resolveKnownHosts(ctx context.Context, c client.Client, namespace string, kh *stokerv1alpha1.KnownHosts) (ssh.HostKeyCallback, error) {
	khSecret := &corev1.Secret{}
	khKey := types.NamespacedName{Name: kh.SecretRef.Name, Namespace: namespace}
	if err := c.Get(ctx, khKey, khSecret); err != nil {
		return nil, fmt.Errorf("getting known_hosts secret %s/%s: %w", namespace, kh.SecretRef.Name, err)
	}
	khData, ok := khSecret.Data[kh.SecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in known_hosts secret %s/%s", kh.SecretRef.Key, namespace, kh.SecretRef.Name)
	}

	tmpFile, err := os.CreateTemp("", "stoker-known-hosts-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp known_hosts file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := tmpFile.Write(khData); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("writing known_hosts: %w", err)
	}
	_ = tmpFile.Close()

	hostKeyCallback, err := knownhosts.New(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("parsing known_hosts: %w", err)
	}
	return hostKeyCallback, nil
}

func resolveTokenAuth(ctx context.Context, c client.Client, namespace string, tokenAuth *stokerv1alpha1.TokenAuth) (transport.AuthMethod, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: tokenAuth.SecretRef.Name, Namespace: namespace}
	if err := c.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting token secret %s/%s: %w", namespace, tokenAuth.SecretRef.Name, err)
	}

	token, ok := secret.Data[tokenAuth.SecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s/%s", tokenAuth.SecretRef.Key, namespace, tokenAuth.SecretRef.Name)
	}

	return &gogithttp.BasicAuth{
		Username: "x-access-token",
		Password: string(token),
	}, nil
}
