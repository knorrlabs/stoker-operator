---
sidebar_position: 5
title: Multi-Site Deployment
description: Run Stoker across multiple sites or environments with per-gateway configuration.
---

# Multi-Site Deployment

This guide covers the patterns for deploying Stoker across multiple sites, environments, or regions, where each gateway needs unique configuration while sharing most of the repository structure.

## The problem: unique systemName

Every Ignition gateway requires a unique `systemName` in `config/resources/local/ignition/system-properties/config.json`. In a multi-gateway deployment, this is the primary blocker for a shared git repository: the file is identical except for the system name field.

Stoker solves this in two ways depending on whether you prefer to modify source files or not:

- **Content templating** (`template: true`): author `{{.GatewayName}}` directly in the JSON source file. See the [Content Templating guide](./content-templating.md).
- **JSON patches** (`patches`): keep source files unmodified in git; the agent sets `systemName` at sync time using a dot-notation path. See the [JSON Patches guide](./json-patches.md).

### Patches approach (source file unchanged)

```yaml
mappings:
  - source: "config/resources/ignition/core"
    destination: "config/resources/ignition/core"
    patches:
      - file: "system-properties/config.json"
        set:
          systemName: "{{ .GatewayName }}"
```

The source file in git stays as `{"systemName": "placeholder", ...}` and the agent injects the correct value per gateway at sync time.

## Repository structure

A well-organized multi-site repository separates shared config from per-gateway overrides:

```
config/
├── shared/                          # Shared across all gateways
│   └── resources/
│       └── core/                    # Ignition core config
├── system-properties/               # One file, templated at sync time
│   └── config.json                  # {"systemName": "{{.GatewayName}}"}
└── overlays/                        # Deployment-mode specific
    ├── frontend/                    # Frontend gateway config
    └── backend/                     # Backend gateway config
projects/
└── ...                              # Ignition project files
```

## One GatewaySync CR per namespace

Each namespace (site, environment) gets its own GatewaySync CR. All gateways in the namespace share the same CR and use profiles to select their configuration.

```
production/
  GatewaySync: site-sync
    → profile: site   (for site-level gateways)
    → profile: area   (for area-level gateways)
```

## Example: single site with two profiles

```yaml
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: site1-sync
  namespace: site1
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
      excludePatterns:
        - "**/.git/"
        - "**/.gitkeep"
        - "**/.resources/**"
        - "**/.uuid"           # IMPORTANT: never sync .uuid — it's gateway identity
    profiles:
      site:
        mappings:
          - source: "config/shared"
            destination: "config/resources/core"
            type: dir
          - source: "config/system-properties"
            destination: "config/resources/local/ignition/system-properties"
            type: dir
            template: true    # each gateway gets its own systemName
          - source: "projects"
            destination: "projects"
            type: dir
            required: true
      area:
        mappings:
          - source: "config/shared"
            destination: "config/resources/core"
            type: dir
          - source: "config/system-properties"
            destination: "config/resources/local/ignition/system-properties"
            type: dir
            template: true
          - source: "projects/area"
            destination: "projects"
            type: dir
            required: true
```

Gateways annotate themselves with the profile they use:

```yaml
# On the Ignition gateway pod (in its Helm values or deployment spec)
annotations:
  stoker.io/inject: "true"
  stoker.io/profile: "area"        # select the area profile
  stoker.io/gateway-name: "area1"  # override auto-detected name
```

## Example: multi-environment with vars

For deployments where the same profile must behave differently per environment, use `vars` in defaults:

```yaml
spec:
  sync:
    defaults:
      vars:
        environment: "production"
        historyProvider: "sql-historian"
    profiles:
      default:
        mappings:
          - source: "config/base"
            destination: "config/resources/core"
            type: dir
            template: true
```

The base config can reference these values:

```json title="config/base/historian.json"
{
  "provider": "{{.Vars.historyProvider}}",
  "environment": "{{.Vars.environment}}"
}
```

Different namespaces (dev, staging, production) each have their own GatewaySync CR with different `defaults.vars`, while sharing the same repository structure.

## Example: StatefulSet with multiple replicas

For StatefulSets where each replica manages a different set of resources, use `{{.PodName}}` to give each replica a unique system name:

```yaml
spec:
  sync:
    profiles:
      default:
        mappings:
          - source: "config/system-properties"
            destination: "config/resources/local/ignition/system-properties"
            type: dir
            template: true   # resolves {{.PodName}} to "ignition-0", "ignition-1", etc.
          - source: "projects"
            destination: "projects"
            type: dir
```

In the template file:

```json
{
  "systemName": "{{.PodName}}",
  "httpPort": 8088
}
```

Each pod (`ignition-0`, `ignition-1`) gets a distinct system name without any pod-specific config in git.

## Promoting across environments with Kargo

Stoker integrates naturally with Kargo + ArgoCD for environment promotion. Kargo promotes a git commit by updating `spec.git.ref` on the GatewaySync CR, or by triggering the [webhook receiver](./webhook-sync.md) with a Kargo notification.

Typical flow:
1. Kargo promotes commit `abc123` to the `production` stage
2. Kargo sends a `POST /webhook/{namespace}/{crName}` with `{"ref": "abc123"}`
3. Stoker resolves the ref and syncs all gateways to that commit
4. Each gateway renders its templates from the commit's file contents

## Namespacing for multi-site

A common pattern for multi-site Kubernetes deployments:

```
cluster/
├── site1/              # namespace
│   └── GatewaySync: site1-sync
├── site2/              # namespace
│   └── GatewaySync: site2-sync
└── site3/
    └── GatewaySync: site3-sync
```

Each site has its own namespace with:
- Its own GatewaySync CR (can track different refs per site)
- Its own GitHub App token Secret (created by the controller)
- Its own gateway API key Secret
- Namespace label `stoker.io/injection=enabled` (only if `webhook.namespaceSelector.requireLabel=true`)

## Important: protect the `.uuid` file

Ignition's `.uuid` file contains the gateway's unique identity. Syncing it would cause two gateways to share the same identity, breaking gateway network routing and historian data.

Always exclude it:

```yaml
spec:
  sync:
    defaults:
      excludePatterns:
        - "**/.git/"
        - "**/.gitkeep"
        - "**/.resources/**"
        - "**/.uuid"            # required — never sync the uuid file
```

## Checklist before deploying

- [ ] `**/.uuid` is in `excludePatterns`
- [ ] `template: true` is set on any mapping that contains `{{...}}` in file contents
- [ ] `patches` are used for targeted JSON field updates (no source-file modification required)
- [ ] `vars` keys in templates or patch values match the keys defined in `spec.sync.defaults.vars` or `spec.sync.profiles.<name>.vars`
- [ ] Each gateway pod has `stoker.io/inject: "true"` and optionally `stoker.io/profile: "<name>"`
- [ ] If `webhook.namespaceSelector.requireLabel=true`, the namespace has label `stoker.io/injection=enabled` (not required with default configuration)
- [ ] Binary files (images, compiled modules) are in a separate mapping **without** `template: true`, or are excluded
- [ ] Gateway API key Secret exists in the namespace
- [ ] Git auth Secret exists in the namespace (GitHub App PEM, SSH key, or token)
