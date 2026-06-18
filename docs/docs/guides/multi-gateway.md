---
sidebar_position: 2
title: Multi-Gateway Profiles
description: Route different gateways to different paths in the same repository.
---

# Multi-Gateway Profiles

When managing multiple Ignition gateways, you typically want each gateway to sync a different subset of the same repository. Stoker provides template variables and named profiles to handle this without duplicating configuration.

## Gateway name routing

Use `{{.GatewayName}}` to route each gateway to its own directory based on its identity. The gateway name comes from the `stoker.io/gateway-name` annotation, or falls back to the `app.kubernetes.io/name` label.

```yaml
spec:
  sync:
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
```

A gateway named `ignition-blue` syncs from `services/ignition-blue/`, while `ignition-red` syncs from `services/ignition-red/`: same profile, different files.

## Label-based routing

Use `{{.Labels.key}}` to route based on any Kubernetes label on the gateway pod. This is useful when gateway names don't match your repository layout.

```yaml
spec:
  sync:
    profiles:
      standard:
        mappings:
          - source: "sites/{{.Labels.site}}/projects/"
            destination: "projects/"
            type: dir
            required: true
          - source: "sites/{{.Labels.site}}/config/"
            destination: "config/"
            type: dir
```

Set the label on the gateway pod (typically via the Helm chart's `podLabels`):

```yaml
# Ignition Helm values
podLabels:
  site: factory-north
```

Now this gateway syncs from `sites/factory-north/`.

:::warning Label key naming
`key` in `{{.Labels.key}}` must be a **valid identifier**: letters, digits, and underscores only. **Dashes and dots are not supported** and cause a template parse error at sync time.

| | |
|---|---|
| `{{.Labels.site}}` | ✅ |
| `{{.Labels.factoryZone}}` | ✅ |
| `{{.Labels.my-label}}` | ❌ parse error |
| `{{.Labels.app.kubernetes.io}}` | ❌ silently wrong (looks up key `"app"`) |

For K8s system labels like `app.kubernetes.io/name`, use `{{.GatewayName}}` or a `vars` entry instead.
:::

## Custom variables

Use `{{.Vars.key}}` with profile-level `vars` for values that don't map to Kubernetes labels or names:

```yaml
spec:
  sync:
    profiles:
      standard:
        vars:
          region: us-east       # valid: letters and dashes not allowed
          tier: production
        mappings:
          - source: "{{.Vars.region}}/{{.Vars.tier}}/projects/"
            destination: "projects/"
            type: dir
```

:::warning Var key naming
Var keys follow the same identifier constraint as label keys: **letters, digits, and underscores only; no dashes**. The controller rejects CRs with invalid var keys immediately at reconcile time with a `ProfilesValid=False` status condition, so you'll catch errors before they reach sync.

| | |
|---|---|
| `region`, `siteNumber`, `projectName` | ✅ |
| `my-var`, `site.name` | ❌ rejected at apply time |

To use a value that contains dashes (e.g. a project name), store it in a var with a valid key:
```yaml
vars:
  projectName: "public-demo"    # stored with valid key; value can contain anything
```
Then reference it as `{{.Vars.projectName}}`. To combine it with a replica ordinal: `"{{.Vars.projectName}}-{{.PodOrdinal}}"`.
:::

## Multiple named profiles

When gateways need fundamentally different mappings (not just different paths), use separate named profiles:

```yaml
spec:
  sync:
    profiles:
      full:
        mappings:
          - source: "projects/"
            destination: "projects/"
            type: dir
          - source: "config/"
            destination: "config/"
            type: dir
          - source: "themes/"
            destination: "themes/"
            type: dir
      config-only:
        mappings:
          - source: "config/"
            destination: "config/"
            type: dir
```

Assign profiles to gateways via the `stoker.io/profile` pod annotation:

```yaml
# Full gateway
podAnnotations:
  stoker.io/inject: "true"
  stoker.io/cr-name: my-sync
  stoker.io/profile: full

# Config-only gateway
podAnnotations:
  stoker.io/inject: "true"
  stoker.io/cr-name: my-sync
  stoker.io/profile: config-only
```

## Shared config with per-gateway overrides

Combine techniques: use one mapping for shared files and another with template variables for per-gateway files. Mappings are applied top to bottom, so later mappings overlay earlier ones.

```yaml
spec:
  sync:
    profiles:
      standard:
        mappings:
          # Shared config for all gateways
          - source: "shared/config/"
            destination: "config/"
            type: dir
          # Per-gateway project files
          - source: "services/{{.GatewayName}}/projects/"
            destination: "projects/"
            type: dir
            required: true
          # Per-gateway config overrides (overlays shared)
          - source: "services/{{.GatewayName}}/config/"
            destination: "config/"
            type: dir
```

## Defaults inheritance

Use `spec.sync.defaults` to set baseline values that all profiles inherit. Profiles only need to specify fields they want to override:

```yaml
spec:
  sync:
    defaults:
      excludePatterns:
        - "**/.git/"
        - "**/.gitkeep"
        - "**/.resources/**"
      syncPeriod: 30
      designerSessionPolicy: proceed
    profiles:
      production:
        mappings:
          - source: "services/{{.GatewayName}}/projects/"
            destination: "projects/"
            type: dir
        # Inherits syncPeriod: 30, excludePatterns, designerSessionPolicy from defaults
        designerSessionPolicy: wait  # Override: wait for designers in production
      development:
        mappings:
          - source: "services/{{.GatewayName}}/projects/"
            destination: "projects/"
            type: dir
        syncPeriod: 10  # Override: faster sync for dev
        # Inherits excludePatterns and designerSessionPolicy: proceed from defaults
```

## Available template variables

| Variable | Source | Example value |
|----------|--------|---------------|
| `{{.GatewayName}}` | `stoker.io/gateway-name` annotation or `app.kubernetes.io/name` label | `ignition-blue` |
| `{{.PodName}}` | Kubernetes pod name | `my-gateway-0` |
| `{{.PodOrdinal}}` | StatefulSet replica index: from `apps.kubernetes.io/pod-index` label (K8s 1.27+), falls back to parsing pod name | `0`, `1`, `2` |
| `{{.Labels.key}}` | Any pod label; key must be a valid identifier (no dashes) | `factory-north` |
| `{{.Vars.key}}` | Profile or defaults `vars` map; key must be a valid identifier (no dashes) | `us-east` |
| `{{.CRName}}` | GatewaySync CR name | `my-sync` |
| `{{.Namespace}}` | Pod namespace | `production` |
| `{{.Ref}}` | Resolved git ref | `main` |
| `{{.Commit}}` | Full commit SHA | `4d19160...` |

See the [CRD reference](../reference/gatewaysync-cr.md#template-variables) for the full variable list and identifier naming rules.

## Next steps

- [GatewaySync CR Reference](../reference/gatewaysync-cr.md#template-variables): full template variable reference
- [Webhook Sync](./webhook-sync.md): trigger instant syncs on push events
