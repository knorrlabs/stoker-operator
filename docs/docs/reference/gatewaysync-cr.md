---
sidebar_position: 1
slug: /reference/gatewaysync-cr
title: GatewaySync CR
description: Full reference for the GatewaySync custom resource.
---

# GatewaySync CR Reference

The `GatewaySync` custom resource defines the git repository to sync from, authentication, polling behavior, gateway connection settings, sync profiles, and agent configuration.

```yaml
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: my-gatewaysync
  namespace: my-namespace
spec:
  git:
    repo: "https://github.com/org/repo.git"
    ref: "main"
    auth:
      token:
        secretRef:
          name: git-token
          key: token
  polling:
    enabled: true
    interval: "60s"
  gateway:
    port: 8088
    tls: false
    api:
      secretName: gw-api-key
  sync:
    defaults:
      excludePatterns:
        - "**/.git/"
        - "**/.gitkeep"
        - "**/.resources/**"
      syncPeriod: 30
      designerSessionPolicy: proceed
    profiles:
      standard:
        mappings:
          - source: "services/{{.GatewayName}}/projects/"
            destination: "projects/"
            type: dir
            required: true
          - source: "services/{{.GatewayName}}/config/"
            destination: "config/"
            type: dir
        syncPeriod: 60
        designerSessionPolicy: wait
  # agent: {}  # optional — sidecar image defaults come from the Helm chart's agentImage values
  paused: false
```

## `spec.git`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `repo` | string | Yes | — | Git repository URL (SSH or HTTPS) |
| `ref` | string | Yes | — | Git reference to sync: branch, tag, or commit SHA |
| `auth` | object | No | — | Git authentication configuration |

### `spec.git.auth`

Exactly one authentication method should be specified. Omit entirely for public repositories.

#### Token authentication

```yaml
auth:
  token:
    secretRef:
      name: git-token
      key: token
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `token.secretRef.name` | string | Yes | Name of the Secret |
| `token.secretRef.key` | string | Yes | Key within the Secret |

#### SSH key authentication

```yaml
auth:
  sshKey:
    secretRef:
      name: ssh-key
      key: id_ed25519
    knownHosts:                # optional — enables host key verification
      secretRef:
        name: ssh-known-hosts
        key: known_hosts
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `sshKey.secretRef.name` | string | Yes | Name of the Secret containing the SSH private key |
| `sshKey.secretRef.key` | string | Yes | Key within the Secret |
| `sshKey.knownHosts.secretRef.name` | string | No | Name of a Secret containing SSH `known_hosts` data |
| `sshKey.knownHosts.secretRef.key` | string | No | Key within the known_hosts Secret |

When `knownHosts` is omitted, SSH connections use `InsecureIgnoreHostKey` (no MITM protection). The controller sets a `SSHHostKeyVerification=False` warning condition to flag this. See the [Git Authentication guide](../guides/git-authentication.md#ssh-host-key-verification) for setup instructions.

#### GitHub App authentication

```yaml
auth:
  githubApp:
    appId: 12345
    installationId: 67890
    privateKeySecretRef:
      name: github-app-key
      key: private-key.pem
    apiBaseURL: "https://github.example.com/api/v3"  # optional, for GitHub Enterprise
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `githubApp.appId` | integer | Yes | — | GitHub App ID |
| `githubApp.installationId` | integer | Yes | — | GitHub App installation ID |
| `githubApp.privateKeySecretRef.name` | string | Yes | — | Name of the Secret containing the PEM key |
| `githubApp.privateKeySecretRef.key` | string | Yes | — | Key within the Secret |
| `githubApp.apiBaseURL` | string | No | — | GitHub API base URL (runtime default: `https://api.github.com`). Set this for GitHub Enterprise Server. |

The controller exchanges the PEM private key for a short-lived installation access token (1-hour expiry), caches it with a 5-minute pre-expiry refresh, and writes it to a controller-managed Secret (`stoker-github-token-{crName}`). The agent mounts this Secret at `/etc/stoker/git-token/token`. The PEM key never leaves the controller namespace; agent pods do not mount the PEM secret.

## `spec.polling`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `enabled` | bool | No | `true` | Whether periodic polling for git changes is active |
| `interval` | string | No | `"60s"` | Polling period (e.g., `"60s"`, `"5m"`) |

:::tip
If you configure a [webhook receiver](/reference/helm-values#push-receiver-webhook) for push-event-driven sync, you can set `polling.enabled: false` or increase the interval to reduce API calls.
:::

## `spec.gateway`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `port` | int32 | No | `8088` | Ignition gateway API port |
| `tls` | bool | No | `false` | Enable TLS for gateway API connections |
| `api.secretName` | string | Yes | — | Name of the Secret containing the Ignition API key |
| `api.secretKey` | string | No | `"apiKey"` | Key within the Secret |

## `spec.sync`

The `sync` section contains baseline defaults and named profiles.

### `spec.sync.defaults`

Baseline settings inherited by all profiles. Individual profiles can override these values.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `excludePatterns` | []string | No | `["**/.git/", "**/.gitkeep", "**/.resources/**"]` | Glob patterns for files to exclude from sync |
| `vars` | map[string]string | No | — | Default template variables inherited by all profiles. Profile `vars` override these per-key. Keys must be valid identifiers (letters, digits, underscores; no dashes). |
| `syncPeriod` | int32 | No | `30` | Agent-side polling interval in seconds (min: 5, max: 3600) |
| `designerSessionPolicy` | string | No | `"proceed"` | Behavior when Designer sessions are active: `proceed`, `wait`, or `fail` |
| `dryRun` | bool | No | `false` | Sync to staging only; writes the diff to the status ConfigMap without modifying `/ignition-data/` |
| `paused` | bool | No | `false` | Halt sync for all profiles |

The `**/.resources/**` pattern is always enforced by the agent even if omitted from `excludePatterns`.

### `spec.sync.profiles`

A map of named sync profiles. Each key is the profile name, referenced by the `stoker.io/profile` pod annotation. Gateways without a `stoker.io/profile` annotation use the profile named `default` if one exists. At least one profile must be defined; the API server rejects an empty map.

```yaml
sync:
  profiles:
    standard:
      mappings:
        - source: "services/{{.GatewayName}}/projects/"
          destination: "projects/"
          type: dir
          required: true
      syncPeriod: 60
    minimal:
      mappings:
        - source: "config/"
          destination: "config/"
          type: dir
```

Each profile supports the following fields:

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mappings` | []object | Yes | — | Ordered list of source-to-destination file mappings |
| `excludePatterns` | []string | No | — | Additional glob patterns merged with `spec.sync.defaults.excludePatterns` |
| `vars` | map[string]string | No | — | Custom template variables available as `{{.Vars.key}}`. Keys must be valid identifiers (letters, digits, underscores; no dashes). |
| `syncPeriod` | int32 | No | inherited | Overrides `spec.sync.defaults.syncPeriod` |
| `dryRun` | bool | No | inherited | Overrides `spec.sync.defaults.dryRun` |
| `designerSessionPolicy` | string | No | inherited | Overrides `spec.sync.defaults.designerSessionPolicy` |
| `paused` | bool | No | inherited | Overrides `spec.sync.defaults.paused` |

#### Mappings

An ordered list of source-to-destination file mappings. Applied top to bottom; later mappings overlay earlier ones.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `source` | string | Yes | — | Repo-relative path to copy from |
| `destination` | string | Yes | — | Path relative to the Ignition data directory (`/ignition-data/`) |
| `type` | string | No | inferred | Entry type: `"dir"` or `"file"`. When omitted the agent infers the type from the filesystem at sync time. |
| `required` | bool | No | `false` | Fail sync if the source path doesn't exist |
| `template` | bool | No | `false` | Resolve Go template variables inside file **contents** at sync time. Binary files (null bytes) are rejected. See [Content Templating](../guides/content-templating.md). |
| `patches` | []object | No | — | Targeted JSON field updates applied at sync time. See [JSON Patches](../guides/json-patches.md). |

:::note
`type` is inferred from `os.Stat` on the source path; no default value is required in the CR. If you set it explicitly, it acts as a validation hint: the agent errors if the actual filesystem type doesn't match. A source that doesn't exist (when `required: false`) defaults to `"dir"` and is silently skipped.
:::

#### `patches`

Each entry in `patches` applies one set of JSON field updates to files matched by the `file` glob:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `file` | string | No | Path relative to the mapping's destination. Supports doublestar globs (`**/*.json`). For **file mappings** (`type: file`), omit to target the mapped file itself. |
| `set` | map[string]string | Yes | sjson dot-notation paths to values. Values support Go template syntax (same variables as `template: true`). |

```yaml
mappings:
  - source: "config/resources/ignition/core"
    destination: "config/resources/ignition/core"
    patches:
      - file: "system-properties/config.json"
        set:
          systemName: "{{ .GatewayName }}"
          httpPort: "{{ .Vars.gatewayPort }}"
      - file: "db-connections/*.json"
        set:
          connection.host: "{{ .Vars.dbHost }}"
```

See the [JSON Patches guide](../guides/json-patches.md) for full examples, type inference rules, and path syntax.

#### Template variables

Both `source` and `destination` support Go template variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `{{.GatewayName}}` | Gateway identity from the `stoker.io/gateway-name` annotation (or `app.kubernetes.io/name` label) | `sites/{{.GatewayName}}/projects` |
| `{{.PodName}}` | Kubernetes pod name | `system-{{.PodName}}` |
| `{{.PodOrdinal}}` | StatefulSet replica index (`0`, `1`, `2`, ...). Always `0` for non-StatefulSet pods. Sourced from the `apps.kubernetes.io/pod-index` label (K8s 1.27+) with pod-name fallback. | `"{{.Vars.projectName}}-{{.PodOrdinal}}"` |
| `{{.CRName}}` | Name of the GatewaySync CR that owns this sync | `config/{{.CRName}}/resources` |
| `{{.Labels.key}}` | Any label on the gateway pod. `key` must be a simple identifier (letters, digits, underscores). See note below. | `sites/{{.Labels.site}}/projects` |
| `{{.Vars.key}}` | Custom variable from profile or defaults `vars` (profile overrides default per-key) | `site{{.Vars.siteNumber}}/scripts` |
| `{{.Namespace}}` | Pod namespace | `config/{{.Namespace}}/overlay` |
| `{{.Ref}}` | Resolved git ref | — |
| `{{.Commit}}` | Full commit SHA | — |

Using `{{.GatewayName}}` or `{{.Labels.key}}` in source paths lets a single profile serve multiple gateways, each syncing from its own directory in the repo.

##### Example: StatefulSet replica systemName with `{{.PodOrdinal}}`

For StatefulSets with multiple replicas, use `{{.PodOrdinal}}` to produce a unique, stable name per replica:

```yaml
sync:
  defaults:
    vars:
      projectName: "my-gateway"   # key must be a valid identifier
  profiles:
    frontend:
      mappings:
        - source: "config/system-properties"
          destination: "config/system-properties"
          type: dir
          patches:
            - file: "config.json"
              set:
                systemName: "{{.Vars.projectName}}-{{.PodOrdinal}}"
                # → my-gateway-0, my-gateway-1, my-gateway-2, ...
```

The `-` between `}}` and `{{` is literal text outside the template delimiters; this is valid syntax even though dashes cannot appear *inside* `{{ }}`.

##### Example: label-based routing

Add a `site` label to each gateway pod and use it in the profile:

```yaml
sync:
  profiles:
    standard:
      mappings:
        - source: "services/{{.Labels.site}}/projects/"
          destination: "projects/"
          type: dir
          required: true
        - source: "services/{{.Labels.site}}/config/"
          destination: "config/"
          type: dir
```

A pod with label `site: ignition-blue` syncs from `services/ignition-blue/`, while `site: ignition-red` syncs from `services/ignition-red/`: same profile, different files.

:::note
**Label and var key naming constraint:** Go's template engine requires map keys to be valid identifiers when accessed with dot notation. Keys must use only letters, digits, and underscores (dashes, dots, and slashes are not supported).

- `{{.Labels.site}}` ✅ (simple identifier)
- `{{.Labels.my-label}}` ❌ (parse error: `bad character '-'`)
- `{{.Labels.app.kubernetes.io}}` ❌ (silently looks up key `"app"`, not `"app.kubernetes.io"`)

For K8s system labels with dots or slashes (e.g., `apps.kubernetes.io/pod-index`), use `{{.PodOrdinal}}` or a `vars` entry instead of `{{.Labels.*}}`.

The same constraint applies to `vars` keys: `{{.Vars.myVar}}` ✅, `{{.Vars.my-var}}` ❌. The controller rejects CRs with invalid var keys at reconcile time with a clear status condition.

`{{.Labels.key}}` reads from the pod's Kubernetes labels at sync time. The agent needs `get` permission on pods (included in the agent ClusterRole).
:::

#### Designer session policy

Controls sync behavior when Ignition Designer sessions are active on the gateway. Can be set at the defaults level or overridden per profile.

| Value | Behavior |
|-------|----------|
| `proceed` (default) | Logs a warning and continues the sync |
| `wait` | Retries until sessions close (up to 5 minutes) |
| `fail` | Aborts the sync |

## `spec.agent`

Omitting `spec.agent` entirely is the normal case. When `spec.agent.image` is not set, the webhook resolves the sidecar image from the `DEFAULT_AGENT_IMAGE` environment variable, which the Helm chart sets via its `agentImage` values. Specify `spec.agent.image.*` only when overriding the chart default.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `image.repository` | string | No | — | Agent container image repository |
| `image.tag` | string | No | — | Agent container image tag |
| `image.pullPolicy` | string | No | — | Image pull policy |
| `resources` | object | No | — | Agent container resource requirements |

## `spec.paused`

When set to `true`, halts all sync operations. The controller continues to reconcile and resolve refs, but agents will not perform syncs.

## Pod Annotations

Gateways are discovered by pod annotations. These are typically set via `podAnnotations` in the Ignition Helm chart values:

| Annotation | Required | Description |
|---|---|---|
| `stoker.io/inject` | Yes | Set to `"true"` to trigger sidecar injection |
| `stoker.io/cr-name` | Yes | Name of the GatewaySync CR to sync from |
| `stoker.io/profile` | No | Name of the sync profile to use (from `spec.sync.profiles`). Falls back to `default` if unset. |
| `stoker.io/gateway-name` | No | Override gateway identity (defaults to pod label `app.kubernetes.io/name`) |

## Status

The GatewaySync CR status is managed by the controller and reports:

| Field | Description |
|-------|-------------|
| `observedGeneration` | Most recent spec generation observed by the controller |
| `lastSyncRef` | The git ref that was last resolved |
| `lastSyncCommit` | Full 40-character git commit SHA |
| `lastSyncCommitShort` | Abbreviated 7-character commit SHA (used in printer columns) |
| `lastSyncTime` | Timestamp of the last commit change (only updates when the resolved commit changes) |
| `refResolutionStatus` | `NotResolved`, `Resolving`, `Resolved`, or `Error` |
| `profileCount` | Number of profiles defined in `spec.sync.profiles` |
| `discoveredGateways` | List of gateway pods discovered by the controller. See [sub-table below](#discoveredgateways-fields). |
| `conditions` | Standard Kubernetes conditions: `RefResolved`, `AllGatewaysSynced`, and `Ready` |

### `discoveredGateways` fields

Each entry in `status.discoveredGateways` represents a single discovered gateway pod:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Gateway identity (from `stoker.io/gateway-name` annotation or `app.kubernetes.io/name` label) |
| `namespace` | string | Namespace of the gateway pod |
| `podName` | string | Name of the gateway pod |
| `serviceAccountName` | string | ServiceAccount used by the gateway pod |
| `profile` | string | Name of the sync profile selected by this gateway |
| `syncStatus` | string | Current sync state: `Pending`, `Synced`, `Error`, or `MissingSidecar` |
| `lastSyncTime` | timestamp | When this gateway was last synced |
| `lastSyncDuration` | string | How long the last sync took (e.g., `"1.23s"`) |
| `syncedCommit` | string | Full git commit SHA currently synced to this gateway |
| `syncedRef` | string | Git ref currently synced to this gateway |
| `agentVersion` | string | Version of the sync agent sidecar on this gateway |
| `lastScanResult` | string | Summary of the last Ignition scan API response |
| `filesChanged` | int32 | Number of files changed in the last sync |
| `projectsSynced` | []string | Ignition project names synced to this gateway |

### Printer columns

`kubectl get gs` shows these columns by default:

```text
NAME         REF    GATEWAYS     READY   STATUS              AGE
my-gateway   main   1/1 synced   True    All gateways synced 5m
```

`kubectl get gs -o wide` adds `COMMIT`, `PROFILES`, and `LAST SYNC`.

### Sync status lifecycle

Gateways progress through these sync states:

1. **Pending:** initial sync completes (files written) but gateway hasn't been validated yet
2. **Synced:** the Ignition scan API confirmed both `/scan/projects` and `/scan/config` returned HTTP 200
3. **Error:** the scan API returned a non-200 status or was unreachable

A gateway can also enter the **MissingSidecar** state when the stoker-agent sidecar container is absent from the discovered pod. This is not a progression from the states above; it indicates the mutating webhook did not inject the sidecar (e.g., the pod predates the webhook or the `stoker.io/inject` annotation is missing).

The `AllGatewaysSynced` condition is `True` only when all discovered gateways report `Synced`.

### Conditions

| Type | Description |
|------|-------------|
| `RefResolved` | The controller successfully resolved the git ref to a commit SHA |
| `ProfilesValid` | All embedded profiles pass validation (no path traversal, no absolute paths) |
| `AllGatewaysSynced` | All discovered gateway pods report `Synced` status |
| `SidecarInjected` | All discovered gateway pods have the stoker-agent sidecar container |
| `SSHHostKeyVerification` | SSH host key verification status: `True` when `knownHosts` is configured, `False` (warning) when SSH auth is used without it. Only present on CRs using SSH key authentication. |
| `Ready` | `RefResolved`, `ProfilesValid`, and `AllGatewaysSynced` are all `True` |
