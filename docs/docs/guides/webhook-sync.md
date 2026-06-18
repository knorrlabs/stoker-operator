---
sidebar_position: 3
title: Webhook Sync
description: Trigger instant syncs on git push events via webhook.
---

# Webhook Sync

By default, Stoker polls for git changes at a configurable interval (default 60s). For faster feedback, configure a webhook so pushes trigger syncs immediately.

## Enable the webhook receiver

The webhook receiver is disabled by default. Enable it in your Helm values:

```yaml
webhookReceiver:
  enabled: true
  hmac:
    secret: "my-webhook-secret"  # recommended for GitHub webhooks
```

Or via `--set`:

```bash
helm upgrade stoker oci://ghcr.io/ia-eknorr/charts/stoker-operator \
  -n stoker-system --set webhookReceiver.enabled=true
```

## How it works

The controller runs an HTTP server (port 9444) that accepts webhook payloads. When a payload arrives, the receiver:

1. Validates auth (HMAC or bearer token if configured; first match wins)
2. Extracts the ref from the payload (auto-detects format)
3. Annotates the GatewaySync CR with the requested ref
4. The controller's reconciliation predicate detects the annotation change and triggers an immediate sync

## Endpoint

```
POST /webhook/{namespace}/{crName}
```

- `{namespace}`: the namespace of the GatewaySync CR
- `{crName}`: the name of the GatewaySync CR

When `webhookReceiver.enabled` is true, the Helm chart creates a Service for the webhook receiver automatically.

## Exposing the receiver

The webhook receiver Service needs to be reachable from your git hosting provider or CI/CD system. Common approaches:

**Ingress via Helm values (recommended):**

```yaml
webhookReceiver:
  enabled: true
  ingress:
    enabled: true
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
    hosts:
      - host: stoker.example.com
        paths:
          - path: /webhook
            pathType: Prefix
    tls:
      - secretName: stoker-webhook-tls
        hosts:
          - stoker.example.com
```

**Port-forward (for testing):**

```bash
kubectl port-forward -n stoker-system svc/stoker-stoker-operator-webhook-receiver 9444:9444
```

## Payload formats

The receiver auto-detects the payload format. No configuration needed.

### GitHub release

```json
{
  "action": "published",
  "release": {
    "tag_name": "v2.0.0"
  }
}
```

### ArgoCD notification

```json
{
  "app": {
    "metadata": {
      "annotations": {
        "git.ref": "v2.0.0"
      }
    }
  }
}
```

### Kargo promotion

```json
{
  "freight": {
    "commits": [
      {
        "tag": "v2.0.0"
      }
    ]
  }
}
```

### Generic

Any system can trigger a sync by sending:

```json
{
  "ref": "v2.0.0"
}
```

## Authentication

Configure at least one auth method for production. If both are set, either method can authorize a request.

### HMAC (GitHub-compatible)

HMAC validates the `X-Hub-Signature-256` header, the standard used by GitHub webhooks. Use this when your sender can compute signatures (GitHub, custom senders).

```yaml
webhookReceiver:
  hmac:
    secret: "my-webhook-secret"        # inline
    # secretRef:                       # or from an existing Secret
    #   name: webhook-hmac
    #   key: webhook-secret
```

### Bearer token

Any HTTP client that can set headers can authenticate with a static bearer token. The receiver validates the `Authorization: Bearer <token>` header; no signature computation required.

```yaml
webhookReceiver:
  token:
    secret: "my-token"                 # inline
    # secretRef:                       # or from an existing Secret
    #   name: webhook-token-secret
    #   key: webhook-token
```

The sender must include the header:

```
Authorization: Bearer <token>
```

:::warning
When enabled without any auth, any client that can reach the endpoint can trigger a reconcile. Always configure HMAC or a bearer token for production use.
:::

## GitHub webhook setup

1. Go to your repository **Settings → Webhooks → Add webhook**
2. Set **Payload URL** to `https://stoker-webhook.example.com/webhook/{namespace}/{crName}`
3. Set **Content type** to `application/json`
4. Set **Secret** to the same value configured in your Helm values
5. Select events: **Releases** (for tag-based deploys) or **Pushes** (for branch-based deploys)
6. Click **Add webhook**

Test with a curl:

```bash
curl -X POST https://stoker-webhook.example.com/webhook/my-namespace/my-sync \
  -H "Content-Type: application/json" \
  -d '{"ref": "v1.0.0"}'
```

## Combining with polling

Webhooks and polling are complementary. A good production pattern:

- Set a **long poll interval** (e.g., `5m`) as a fallback in case a webhook is missed
- Use **webhooks** for instant sync on push events

```yaml
spec:
  polling:
    enabled: true
    interval: "5m"  # Fallback only — webhooks handle normal flow
```

To disable polling entirely when relying solely on webhooks:

```yaml
spec:
  polling:
    enabled: false
```

## Next steps

- [Helm Values](../reference/helm-values.md#push-receiver-webhook): webhook receiver configuration
- [Annotations Reference](../reference/annotations.md): CR annotations set by the receiver
