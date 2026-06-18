---
sidebar_position: 3
title: Content Templating
description: Resolve gateway-specific values inside synced file contents at sync time.
---

# Content Templating

Content templating lets the agent resolve Go template variables **inside file contents** as files are staged, without modifying source files in git. This solves the most common multi-site problem: each gateway needs subtly different configuration (system name, remote gateway URLs, historian settings) but maintaining one file per gateway in git doesn't scale.

## How it works

Add `template: true` to any mapping. The agent copies the file from the repo into staging, then rewrites it in-place by executing it as a Go template before writing to `/ignition-data`.

```yaml
spec:
  sync:
    profiles:
      default:
        vars:
          systemName: "my-gateway"
        mappings:
          - source: "config/system-properties"
            destination: "config/resources/local/ignition/system-properties"
            type: dir
            template: true   # <-- enable content templating
```

Any file in `config/system-properties/` that contains `{{...}}` syntax is rendered before being written to the gateway. Files without template syntax are copied as-is (fast path).

## Available template variables

| Variable | Example | Description |
|----------|---------|-------------|
| `{{.GatewayName}}` | `ignition-site1` | Gateway identity from annotation or `app.kubernetes.io/name` label |
| `{{.PodName}}` | `ignition-0` | Kubernetes pod name (useful for StatefulSet replicas) |
| `{{.PodOrdinal}}` | `0` | StatefulSet replica index (from `apps.kubernetes.io/pod-index` label with pod-name fallback; always `0` for non-StatefulSet pods) |
| `{{.Namespace}}` | `production` | Kubernetes namespace of the gateway pod |
| `{{.CRName}}` | `site1-sync` | Name of the GatewaySync CR |
| `{{.Ref}}` | `refs/heads/main` | Git ref being synced |
| `{{.Commit}}` | `abc1234` | Full git commit SHA |
| `{{.Labels.key}}` | `factory-north` | Any pod label |
| `{{.Vars.key}}` | `production` | Profile-level or default-level variable |

## The systemName use case

Each Ignition gateway must have a unique `systemName` in `config/resources/local/ignition/system-properties/config.json`. Without content templating you'd need one file per gateway in git:

```
config/
  system-properties/
    site1/config.json   # {"systemName": "site1-gw1"}
    site2/config.json   # {"systemName": "site2-gw1"}
    ...                 # 20 near-identical files
```

With content templating, one file in git handles all gateways:

```json title="config/system-properties/config.json"
{
  "systemName": "{{.GatewayName}}",
  "httpPort": 8088,
  "useSSL": false
}
```

Configure the mapping to template this directory:

```yaml
spec:
  sync:
    profiles:
      default:
        mappings:
          - source: "config/system-properties"
            destination: "config/resources/local/ignition/system-properties"
            type: dir
            template: true
```

Each gateway receives its own rendered version with its unique system name, resolved at sync time.

## Using `vars` for environment-specific values

Combine `template: true` with `vars` to inject deployment-mode or region-specific values.

### vars in defaults (shared across all profiles)

```yaml
spec:
  sync:
    defaults:
      vars:
        region: "us-east-1"
        environment: "production"
    profiles:
      frontend:
        vars:
          deploymentMode: "frontend"   # overrides nothing from defaults
        mappings:
          - source: "config/base"
            destination: "config/resources/core"
            type: dir
            template: true
      backend:
        vars:
          deploymentMode: "backend"
        mappings:
          - source: "config/base"
            destination: "config/resources/core"
            type: dir
            template: true
```

A file in `config/base/config.json` can now reference both profile-level and default-level vars:

```json
{
  "systemName": "{{.GatewayName}}",
  "region": "{{.Vars.region}}",
  "deploymentMode": "{{.Vars.deploymentMode}}",
  "environment": "{{.Vars.environment}}"
}
```

**Merge semantics:** Profile `vars` override default `vars` on a per-key basis. Keys in defaults but not in the profile are inherited; keys in the profile override the default value.

## StatefulSet replica identity with `{{.PodName}}`

For StatefulSets with multiple replicas, each pod needs a unique system name. Use `{{.PodName}}` which resolves to the pod's Kubernetes name (e.g., `ignition-0`, `ignition-1`):

```json title="config/system-properties/config.json"
{
  "systemName": "{{.PodName}}",
  "httpPort": 8088
}
```

For StatefulSets, `{{.PodName}}` is the ordinal-suffixed name from the downward API. This gives each replica a deterministic, unique system name without any manual configuration.

## Deployment mode overlay

Use vars to select a configuration overlay directory at sync time:

```yaml
spec:
  sync:
    profiles:
      frontend:
        vars:
          deploymentMode: "frontend"
        mappings:
          - source: "config/base"
            destination: "config/resources/core"
            type: dir
          - source: "config/overlays/{{.Vars.deploymentMode}}"
            destination: "config/resources/{{.Vars.deploymentMode}}"
            type: dir
```

The `{{.Vars.deploymentMode}}` in the **source path** selects `config/overlays/frontend/` from git, a directory that only exists for this profile. The destination path separates overlays from core config so Ignition reads from both directories at runtime.

## Alternative: JSON patches for targeted field updates

To update only a few specific fields without authoring `{{...}}` syntax in source files, use [JSON Patches](./json-patches.md) instead. Specify an sjson dot-notation path and value; the agent applies the change after staging without touching the rest of the file.

| Approach | When to use |
|----------|-------------|
| `template: true` | Source files are authored with `{{...}}` syntax; works on any text format |
| `patches` | You want to override specific JSON field values without modifying source files |

Both can be used on the same mapping: `template: true` runs first, then patches are applied.

## Binary file protection

Files containing null bytes (`\x00`) are **rejected with an error** when `template: true` is set. This prevents accidental corruption of images, compiled modules, or other binary files.

If a mapping includes both text and binary files, either:
1. Use separate mappings: one with `template: true` for text, one without for binary files
2. Move binary assets to a path excluded by `excludePatterns`

## Error messages

| Error | Cause | Fix |
|-------|-------|-----|
| `template=true on binary file is not supported: <path>` | File contains null bytes | Use a separate mapping for binary files |
| `executing template "<tmpl>": map has no entry for key "<key>"` | `{{.Vars.key}}` references a key not in `vars` | Add the missing key to `vars` in the profile or defaults |
| `parsing template "<tmpl>": ...` | Invalid Go template syntax in file | Fix the `{{...}}` syntax in the source file |

## Full example

```yaml
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: multi-site-sync
  namespace: production
spec:
  git:
    repo: "https://github.com/org/ignition-config.git"
    ref: "main"
    auth:
      githubApp:
        appId: 123456
        installationId: 789012
        privateKeySecretRef:
          name: github-app-key
          key: privateKey
  gateway:
    port: 8088
    api:
      secretName: gw-api-key
  sync:
    defaults:
      vars:
        environment: "production"
        historyProvider: "db-historian"
      excludePatterns:
        - "**/.git/"
        - "**/.gitkeep"
        - "**/.resources/**"
    profiles:
      site:
        vars:
          deploymentMode: "standard"
        mappings:
          - source: "config/shared"
            destination: "config/resources/core"
            type: dir
          - source: "config/system-properties"
            destination: "config/resources/local/ignition/system-properties"
            type: dir
            template: true   # resolves {{.GatewayName}} in system name
          - source: "projects"
            destination: "projects"
            type: dir
            required: true
```
