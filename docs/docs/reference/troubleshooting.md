---
sidebar_position: 4
title: Troubleshooting
description: Common issues, debug commands, and FAQ.
---

# Troubleshooting

## Common issues

### Sidecar not injected

**Symptoms:** Gateway pod has only 1 container, no `stoker-agent` init container.

**Checklist:**

1. **Namespace label:** if `webhook.namespaceSelector.requireLabel=true` is set, ensure the namespace has `stoker.io/injection=enabled`:
   ```bash
   kubectl get namespace <ns> --show-labels
   ```
   With the default configuration, no namespace label is needed; injection works in all namespaces except `kube-system` and `kube-node-lease`.
2. **Pod annotation:** ensure the pod has `stoker.io/inject: "true"` (must be a string, not a boolean):
   ```bash
   kubectl get pod <pod> -n <ns> -o jsonpath='{.metadata.annotations}'
   ```
3. **Webhook running:** check the controller pod logs for webhook server startup:
   ```bash
   kubectl logs -n stoker-system deploy/stoker-stoker-operator-controller-manager | grep webhook
   ```
4. **cert-manager certificates:** the webhook requires a valid TLS certificate:
   ```bash
   kubectl get certificate -n stoker-system
   ```
5. **Timing:** the webhook only injects on pod creation. If the pod was created before the operator was installed, delete the pod and let the StatefulSet recreate it.

### Status stuck at Pending

**Symptoms:** `kubectl get gs` shows READY=False and STATUS shows "Waiting for gateways to sync", but RefResolved is True.

**Checklist:**

1. **Agent logs:** check for errors in the agent sidecar:
   ```bash
   kubectl logs <pod> -n <ns> -c stoker-agent --tail=50
   ```
2. **RBAC:** verify the agent RoleBinding exists (created automatically when `rbac.autoBindAgent.enabled=true`):
   ```bash
   kubectl get rolebinding -n <ns> | grep stoker-agent
   ```
   If auto-RBAC is disabled, create the binding manually:
   ```bash
   kubectl create rolebinding stoker-agent -n <ns> \
     --clusterrole=stoker-agent --serviceaccount=<ns>:<service-account>
   ```
3. **Secret mounts:** if using private repos, verify the git credentials secret exists:
   ```bash
   kubectl get secret -n <ns>
   ```
4. **API key:** verify the Ignition API key secret exists and is referenced correctly:
   ```bash
   kubectl get secret gw-api-key -n <ns>
   ```

### RefResolved=False

**Symptoms:** `kubectl describe gs <name>` shows `RefResolved=False` with an error message.

**Causes:**

- **Invalid repo URL:** check for typos in `spec.git.repo`
- **Auth failure:** the token/SSH key/GitHub App credentials are wrong or expired
- **Network access:** the controller pod can't reach the git host (check network policies)
- **Ref doesn't exist:** the specified branch or tag doesn't exist in the remote
- **SSH host key mismatch:** if `knownHosts` is configured and the git server's key doesn't match, the connection is rejected. Re-scan the host: `ssh-keyscan <host> > known_hosts` and update the Secret.
- **GitHub App exchange failed:** if the condition reason is `GitHubAppExchangeFailed`, check that the App ID, Installation ID, and PEM key are correct. Verify the app has **Contents: Read** permission and is installed on the target repository. Clock skew >60s between the controller and GitHub can also cause JWT validation failures.

The condition message includes the retry interval (e.g., `retry in 30s`). Consecutive failures back off exponentially (30s → 60s → 120s → 240s → 5min cap). Fixing the root cause or triggering a webhook resets the backoff immediately.

Check controller logs for the specific error:

```bash
kubectl logs -n stoker-system deploy/stoker-stoker-operator-controller-manager | grep "ls-remote\|GitHub App"
```

### SSHHostKeyVerification=False

**Symptoms:** `kubectl describe gs <name>` shows `SSHHostKeyVerification=False` with reason `HostKeyVerificationDisabled`.

**Cause:** SSH key auth is configured without `knownHosts`, so connections use `InsecureIgnoreHostKey`. Connections are vulnerable to MITM attacks.

**Fix:** Add a `knownHosts` Secret. See the [SSH host key verification](../guides/git-authentication.md#ssh-host-key-verification) guide.

### AllGatewaysSynced=False

**Symptoms:** Ref is resolved but gateways show sync errors.

**Causes:**

- **Scan API failure:** check gateway port and TLS settings match `spec.gateway.port` and `spec.gateway.tls`
- **API key format:** the Ignition API key must be in `name:secret` format (e.g., `ignition-api-key:CYCSdRg...`), not just the secret portion
- **Gateway not ready:** the Ignition gateway may still be starting up

```bash
kubectl logs <pod> -n <ns> -c stoker-agent | grep -i "scan\|error"
```

### Scan failures (non-200 response)

**Symptoms:** Agent logs show scan errors with non-200 status codes.

| Code | Likely cause |
|------|-------------|
| 401 | API key missing, wrong format, or not loaded by gateway |
| 404 | Wrong gateway port. The API is on a different port than configured. |
| 500 | Gateway internal error. Check the Ignition gateway logs. |
| Connection refused | Wrong port, TLS mismatch, or gateway not yet started |

:::tip API key format
The Ignition REST API uses a custom header format: `X-Ignition-API-Token: name:secret`. Make sure the secret value includes both the token name and the secret, separated by a colon.
:::

### Content templating errors

**Symptoms:** Agent logs show `templating <path>: ...` errors, sync aborts.

#### Binary file rejected

```
templating config/resources/core/image.png: template=true on binary file is not supported: /ignition-data/.sync-staging/...
```

**Cause:** A mapping with `template: true` matched a binary file (contains null bytes).

**Fix:** Split the mapping into two: one for text files with `template: true`, another for binary files without it. Or exclude binary files with `excludePatterns`:

```yaml
mappings:
  - source: "config/resources/core"
    destination: "config/resources/core"
    type: dir
    template: true
    # If binaries are in a subdirectory:
excludePatterns:
  - "config/resources/core/images/**"
```

#### Undefined template variable

```
templating config/system-properties/config.json: resolving template in .../config.json: template: ...: map has no entry for key "siteCode"
```

**Cause:** A `{{.Vars.siteCode}}` expression references a key that doesn't exist in `spec.sync.defaults.vars` or the profile's `vars`.

**Fix:** Add the missing key to your vars:

```yaml
sync:
  defaults:
    vars:
      siteCode: "site1"   # add the missing key
```

#### Template syntax error

```
templating config/some-file.json: resolving template in .../some-file.json: template: ...: unexpected "}" in operand
```

**Cause:** Malformed Go template syntax in a file synced with `template: true`. Common causes: JSON files that happen to contain `{` or `}` characters in string values (e.g., regex patterns, JavaScript objects in config).

**Fix:** Either escape the braces (`{{"{{"}}` renders as `{{`) or remove `template: true` from this mapping if the file doesn't need variable substitution.

#### GitHub App token Secret missing

**Symptoms:** Agent logs show `open /etc/stoker/git-token/token: no such file or directory` or auth errors on git clone.

**Checklist:**

1. Verify the controller-managed Secret exists:
   ```bash
   kubectl get secret stoker-github-token-<crName> -n <ns>
   ```
2. If missing, check controller logs for token exchange errors:
   ```bash
   kubectl logs -n stoker-system deploy/stoker-stoker-operator-controller-manager | grep "GitHub App\|token"
   ```
3. Verify the GitHub App has **Contents: Read** permission on the repository and is installed on the target repo.
4. Check for clock skew: the JWT used for token exchange is valid for 60 seconds. A controller with clock skew >60s relative to GitHub will get 401 errors.

### JSON patch errors

**Symptoms:** Agent logs show `patching <destination>: ...` errors, sync aborts.

#### File is not valid JSON

```
patching config/db-connections: applying patch to connections.json at "host": file is not valid JSON
```

**Cause:** The matched file isn't parseable as JSON. Common causes: the file is a `.properties` or `.conf` format that isn't JSON, or it contains a JSON syntax error.

**Fix:** Narrow the `file` glob so it only matches actual JSON files, or fix the JSON syntax error in the source file.

#### Invalid patch file pattern

```
patching config/resources: invalid patch file pattern "db-connections/[*.json": ...
```

**Cause:** The `file` field contains a malformed glob (e.g., unclosed `[`).

**Fix:** Correct the glob syntax. Use `**/*.json` for recursive matching or `*.json` for the immediate directory.

#### Undefined template variable in patch value

```
patching config/resources: resolving patch value for path "host": executing template "{{ .Vars.dbHost }}": map has no entry for key "dbHost"
```

**Cause:** A `set` value references `{{ .Vars.dbHost }}` but `dbHost` is not defined in `spec.sync.defaults.vars` or the profile's `vars`.

**Fix:** Add the missing key to your vars:

```yaml
sync:
  defaults:
    vars:
      dbHost: "postgres.svc.cluster.local"   # add the missing key
```

#### Type mismatch

```
mapping[0]: type mismatch: spec says "dir" but /repo/config/versions/.versions.json is a file
```

**Cause:** The `type` field was explicitly set in the CR, but the source path is a different type (file vs directory) on the filesystem.

**Fix:** Remove the `type` field to let the agent infer it automatically, or correct the value to match the actual source (`"file"` for a single file, `"dir"` for a directory).

### Agent CrashLoopBackOff

**Symptoms:** The `stoker-agent` container repeatedly crashes.

**Checklist:**

1. **Previous logs:** check the last crash output:
   ```bash
   kubectl logs <pod> -n <ns> -c stoker-agent --previous
   ```
2. **Resource limits:** the default agent has no resource limits. If limits are set too low, OOM kills can occur.
3. **Volume mounts:** the agent requires `/ignition-data/` to be mounted from the gateway's data volume.
4. **ConfigMap missing:** if the GatewaySync CR was deleted while the pod is running, the metadata ConfigMap no longer exists.

## Debug commands

Quick reference for common debugging commands:

```bash
# Check GatewaySync CR status
kubectl get gs -n <ns>
kubectl get gs -n <ns> -o wide  # includes COMMIT, PROFILES, LAST SYNC columns

# Detailed CR status with conditions
kubectl describe gs <name> -n <ns>

# Agent sidecar logs
kubectl logs <pod> -n <ns> -c stoker-agent --tail=50

# Controller logs
kubectl logs -n stoker-system deploy/stoker-stoker-operator-controller-manager --tail=50

# What the controller sent to the agent
kubectl get cm stoker-metadata-<crName> -n <ns> -o yaml

# GitHub App token Secret (controller-managed)
kubectl get secret stoker-github-token-<crName> -n <ns>

# What the agent reported back (includes sync status and file change details)
kubectl get cm stoker-status-<crName> -n <ns> -o jsonpath='{.data}' | python3 -m json.tool

# Recent events in the namespace
kubectl get events -n <ns> --sort-by=.lastTimestamp | tail -20

# Check webhook certificate status
kubectl get certificate -n stoker-system

# Verify sidecar injection on a pod
kubectl get pod <pod> -n <ns> -o jsonpath='{.spec.initContainers[*].name}'
```
