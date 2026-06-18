---
sidebar_position: 3
title: Annotations & Labels
description: Complete reference for all Stoker annotations and labels.
---

# Annotations & Labels Reference

Stoker uses annotations and labels to control sidecar injection, gateway discovery, and sync behavior. This page documents every annotation and label recognized by the system.

## Pod annotations (set by users)

These annotations are set on gateway pods, typically via `podAnnotations` in the Ignition Helm chart.

| Annotation | Value | Required | Description |
|------------|-------|----------|-------------|
| `stoker.io/inject` | `"true"` | Yes | Triggers sidecar injection by the mutating webhook |
| `stoker.io/cr-name` | string | No | Name of the GatewaySync CR to sync from. Auto-derived if exactly one CR exists in the namespace. |
| `stoker.io/profile` | string | No | Sync profile name from `spec.sync.profiles`. Falls back to the `default` profile if unset. |
| `stoker.io/gateway-name` | string | No | Override gateway identity. Defaults to the pod's `app.kubernetes.io/name` label. |

**Example:**

```yaml
podAnnotations:
  stoker.io/inject: "true"
  stoker.io/cr-name: my-sync
  stoker.io/profile: standard
```

:::tip
Use `--set-string` (not `--set`) when passing annotation values through Helm to avoid boolean coercion (e.g., `"true"` becoming `true`).
:::

## Namespace labels

| Label | Value | Description |
|-------|-------|-------------|
| `stoker.io/injection` | `enabled` | Optional: only needed when `webhook.namespaceSelector.requireLabel=true`. Marks the namespace for sidecar injection. |

```bash
kubectl label namespace my-namespace stoker.io/injection=enabled
```

By default, the webhook intercepts pod creates in all namespaces except `kube-system` and `kube-node-lease`. The namespace label is only required when `webhook.namespaceSelector.requireLabel` is set to `true` in the Helm values.

## CR annotations (set by webhook receiver)

These annotations are set automatically on GatewaySync CRs by the webhook receiver. Users should not set them manually.

| Annotation | Value | Description |
|------------|-------|-------------|
| `stoker.io/requested-ref` | string | Git ref requested by the last webhook payload. Acts as a fast-path override of `spec.git.ref`: the controller uses this value immediately without waiting for ArgoCD to sync. Automatically cleared once `spec.git.ref` catches up (with `v`-prefix normalization). |
| `stoker.io/requested-at` | RFC 3339 timestamp | When the webhook request was received |
| `stoker.io/requested-by` | `"github"`, `"argocd"`, `"kargo"`, or `"generic"` | Source format detected from the payload |

These annotations trigger an immediate reconciliation via the controller's predicate filter. The `requested-ref` annotation is self-cleaning: once `spec.git.ref` is updated (typically by ArgoCD syncing the values change), the controller removes the annotation so it doesn't permanently override the spec. Leaving it in place would pin the controller to a stale ref if a future webhook never fires. The comparison strips a leading `v` from both sides so a git tag like `v2.2.3` matches a `spec.git.ref` value of `2.2.3`.

## Pod labels (set by webhook)

These labels are added to gateway pods by the mutating webhook at injection time. They are not annotations and should not be set manually.

| Label | Value | Description |
|-------|-------|-------------|
| `stoker.io/agent` | `"true"` | Added to every pod that receives a stoker-agent sidecar. The PodMonitor uses this label as its pod selector for metrics scraping. Labels are indexed by Kubernetes; annotations are not, which is why discovery uses a label rather than an annotation. |

## Internal annotations (set by webhook)

| Annotation | Value | Description |
|------------|-------|-------------|
| `stoker.io/injected` | `"true"` | Set by the mutating webhook after successful sidecar injection. Used for tracking; do not set manually. |

## Annotations and labels on owned resources

| Key | Type | Value | Set on | Description |
|-----|------|-------|--------|-------------|
| `stoker.io/cr-name` | Label | CR name | ConfigMaps, Secrets | Identifies the parent GatewaySync CR that owns this resource |
| `stoker.io/gatewaysync` | Label | CR name | RoleBindings | Set on auto-created agent RoleBindings. Value is the GatewaySync CR name that triggered the binding. |
| `stoker.io/secret-type` | Annotation | `"github-app-token"` | Secrets | Marks controller-managed Secrets with their purpose |

## Agent image resolution order

The agent sidecar image is resolved using a two-tier fallback:

1. CR field `spec.agent.image` (highest priority)
2. Environment variable `DEFAULT_AGENT_IMAGE` (set by Helm chart)
