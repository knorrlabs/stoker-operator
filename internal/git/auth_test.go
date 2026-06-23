package git

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"testing"

	"crypto/x509"

	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	stokerv1alpha1 "github.com/knorrlabs/stoker-operator/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = stokerv1alpha1.AddToScheme(s)
	return s
}

func generateTestSSHKey(t *testing.T) []byte {
	t.Helper()
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ed25519 key: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		t.Fatalf("marshaling private key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
}

func generateKnownHostsEntry(t *testing.T) ([]byte, ssh.Signer) {
	t.Helper()
	_, hostPrivKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostPrivKey)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}
	pubKey := signer.PublicKey()
	// Format: "hostname ssh-ed25519 <base64>"
	entry := fmt.Sprintf("localhost %s", ssh.MarshalAuthorizedKey(pubKey))
	return []byte(entry), signer
}

func TestResolveSSHAuth_WithoutKnownHosts(t *testing.T) {
	pemData := generateTestSSHKey(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ssh-key", Namespace: "default"},
		Data:       map[string][]byte{"key": pemData},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(secret).Build()

	sshAuth := &stokerv1alpha1.SSHKeyAuth{
		SecretRef: stokerv1alpha1.SecretKeyRef{Name: "ssh-key", Key: "key"},
	}
	auth, err := resolveSSHAuth(context.Background(), c, "default", sshAuth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pk, ok := auth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *gogitssh.PublicKeys, got %T", auth)
	}

	// Without knownHosts, should accept any host key (InsecureIgnoreHostKey).
	// Verify by calling with a random address and key — should not error.
	_, hostSigner := generateKnownHostsEntry(t)
	err = pk.HostKeyCallback("localhost:22", &net.TCPAddr{}, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("InsecureIgnoreHostKey should accept any key, got: %v", err)
	}
}

func TestResolveSSHAuth_WithKnownHosts(t *testing.T) {
	pemData := generateTestSSHKey(t)
	knownHostsData, hostSigner := generateKnownHostsEntry(t)

	sshSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ssh-key", Namespace: "default"},
		Data:       map[string][]byte{"key": pemData},
	}
	khSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "known-hosts", Namespace: "default"},
		Data:       map[string][]byte{"known_hosts": knownHostsData},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sshSecret, khSecret).Build()

	sshAuth := &stokerv1alpha1.SSHKeyAuth{
		SecretRef: stokerv1alpha1.SecretKeyRef{Name: "ssh-key", Key: "key"},
		KnownHosts: &stokerv1alpha1.KnownHosts{
			SecretRef: stokerv1alpha1.SecretKeyRef{Name: "known-hosts", Key: "known_hosts"},
		},
	}
	auth, err := resolveSSHAuth(context.Background(), c, "default", sshAuth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pk, ok := auth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("expected *gogitssh.PublicKeys, got %T", auth)
	}

	// Should accept the matching host key.
	err = pk.HostKeyCallback("localhost:22", &net.TCPAddr{}, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("expected known host to be accepted, got: %v", err)
	}

	// Should reject an unknown host key.
	_, unknownSigner := generateKnownHostsEntry(t)
	err = pk.HostKeyCallback("localhost:22", &net.TCPAddr{}, unknownSigner.PublicKey())
	if err == nil {
		t.Fatal("expected unknown host key to be rejected")
	}
}

func TestResolveSSHAuth_MissingKnownHostsSecret(t *testing.T) {
	pemData := generateTestSSHKey(t)
	sshSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ssh-key", Namespace: "default"},
		Data:       map[string][]byte{"key": pemData},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sshSecret).Build()

	sshAuth := &stokerv1alpha1.SSHKeyAuth{
		SecretRef: stokerv1alpha1.SecretKeyRef{Name: "ssh-key", Key: "key"},
		KnownHosts: &stokerv1alpha1.KnownHosts{
			SecretRef: stokerv1alpha1.SecretKeyRef{Name: "nonexistent", Key: "known_hosts"},
		},
	}
	_, err := resolveSSHAuth(context.Background(), c, "default", sshAuth)
	if err == nil {
		t.Fatal("expected error for missing known_hosts secret")
	}
}
