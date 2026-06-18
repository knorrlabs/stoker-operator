---
sidebar_position: 1
title: Git Authentication
description: Configure token, SSH, or GitHub App authentication for private repositories.
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';

# Git Authentication

Stoker supports three authentication methods for private Git repositories. Public repositories need no auth configuration; just set `spec.git.repo` and `spec.git.ref`.

<Tabs>
<TabItem value="token" label="Token" default>

## Token authentication

Use a personal access token (classic or fine-grained) for HTTPS repositories. This is the simplest method for GitHub, GitLab, and Bitbucket.

Create a secret containing the token:

```bash
kubectl create secret generic git-token -n <namespace> \
  --from-literal=token=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Reference it in the GatewaySync CR:

```yaml
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: my-sync
  namespace: my-namespace
spec:
  git:
    repo: "https://github.com/org/private-repo.git"
    ref: "main"
    auth:
      token:
        secretRef:
          name: git-token
          key: token
  # ... gateway, sync config
```

**When to use:** Quick setup, CI-generated tokens, or when SSH is blocked by network policy.

:::tip Fine-grained tokens
GitHub fine-grained tokens let you scope access to a single repository with read-only permissions. This is the recommended approach for production.
:::

</TabItem>
<TabItem value="ssh" label="SSH Key">

## SSH key authentication

Use an SSH key for repositories accessed via `git@` URLs.

Generate a deploy key:

```bash
ssh-keygen -t ed25519 -f stoker-deploy-key -N "" -C "stoker"
```

Add the public key (`stoker-deploy-key.pub`) as a read-only deploy key in your repository settings.

Create a secret from the private key:

```bash
kubectl create secret generic ssh-key -n <namespace> \
  --from-file=id_ed25519=stoker-deploy-key
```

Reference it in the GatewaySync CR:

```yaml
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: my-sync
  namespace: my-namespace
spec:
  git:
    repo: "git@github.com:org/private-repo.git"
    ref: "main"
    auth:
      sshKey:
        secretRef:
          name: ssh-key
          key: id_ed25519
  # ... gateway, sync config
```

**When to use:** Organizations that prefer SSH-based access or need deploy keys scoped to individual repositories.

### SSH host key verification

By default, SSH connections skip host key verification (`InsecureIgnoreHostKey`). The controller flags this with a `SSHHostKeyVerification=False` warning condition. To enable strict verification, provide a `known_hosts` Secret:

```bash
# Scan the git host and create a Secret
ssh-keyscan github.com > known_hosts
kubectl create secret generic ssh-known-hosts -n <namespace> \
  --from-file=known_hosts=known_hosts
```

Then reference it in the CR:

```yaml
auth:
  sshKey:
    secretRef:
      name: ssh-key
      key: id_ed25519
    knownHosts:
      secretRef:
        name: ssh-known-hosts
        key: known_hosts
```

When `knownHosts` is configured, the condition changes to `SSHHostKeyVerification=True` and both the controller and agent use strict host key checking. If the git server's key doesn't match the `known_hosts` data, the connection is rejected.

:::tip GitHub Enterprise
For GitHub Enterprise Server, scan your enterprise host: `ssh-keyscan github.example.com > known_hosts`.
:::

</TabItem>
<TabItem value="github-app" label="GitHub App">

## GitHub App authentication

Use a GitHub App for fine-grained, organization-wide access without personal tokens.

### Setup

1. [Create a GitHub App](https://docs.github.com/en/apps/creating-github-apps) with **Contents: Read** permission
2. Install the app on the repository (or organization)
3. Note the **App ID** and **Installation ID** from the app settings
4. Generate a private key and download the PEM file

Create a secret from the PEM key:

```bash
kubectl create secret generic github-app-key -n <namespace> \
  --from-file=private-key.pem=your-app-key.pem
```

Reference it in the GatewaySync CR:

```yaml
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: my-sync
  namespace: my-namespace
spec:
  git:
    repo: "https://github.com/org/private-repo.git"
    ref: "main"
    auth:
      githubApp:
        appId: 12345
        installationId: 67890
        privateKeySecretRef:
          name: github-app-key
          key: private-key.pem
        # apiBaseURL: "https://github.example.com/api/v3"  # GitHub Enterprise only
  # ... gateway, sync config
```

**How it works:** The controller exchanges the PEM private key for a short-lived installation access token (1-hour expiry) via the GitHub API, caches it with a 5-minute pre-expiry refresh, and writes it to a controller-managed Secret (`stoker-github-token-{crName}`). The agent mounts this Secret to authenticate git operations. The PEM key never leaves the controller namespace; agent pods do not mount the PEM secret.

**When to use:** Organizations managing many repos, where individual tokens are impractical or against policy. App tokens auto-rotate and provide audit trails.

:::tip GitHub Enterprise Server
Set `apiBaseURL` to your GitHub Enterprise API endpoint (e.g., `https://github.example.com/api/v3`). Defaults to `https://api.github.com` when omitted.
:::

</TabItem>
</Tabs>

## Auth method comparison

| Method | Protocol | Scope | Rotation | Agent credential |
|--------|----------|-------|----------|-----------------|
| Token | HTTPS | Per-token | Manual | Mounted Secret |
| SSH key | SSH | Per-repo (deploy key) | Manual | Mounted Secret |
| GitHub App | HTTPS | Per-installation | Automatic (1hr) | Controller-managed Secret (PEM never mounted) |

## Next steps

- [GatewaySync CR Reference](../reference/gatewaysync-cr.md#specgitauth): full auth field reference
- [Multi-Gateway Profiles](./multi-gateway.md): route different gateways to different repo paths
