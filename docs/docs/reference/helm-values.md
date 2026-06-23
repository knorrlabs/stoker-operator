---
sidebar_position: 2
slug: /reference/helm-values
title: Helm Values
description: All configurable values for the Stoker operator Helm chart.
---

# Helm Values Reference

The Stoker operator is installed via Helm:

```bash
helm install stoker oci://ghcr.io/knorrlabs/charts/stoker-operator \
  -n stoker-system --create-namespace
```

## All Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | int | `1` | Number of controller replicas. Only one replica holds the leader lock at a time; additional replicas provide fast failover. |
| `image.repository` | string | `ghcr.io/knorrlabs/stoker-operator` | Image repository for the controller manager. |
| `image.tag` | string | `""` | Image tag. Defaults to the chart's appVersion if empty. |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | list | `[]` | Credentials for private container registries. |
| `nameOverride` | string | `""` | Override the chart name used in resource names. |
| `fullnameOverride` | string | `""` | Override the full release name. |
| `agentImage.repository` | string | `ghcr.io/knorrlabs/stoker-agent` | Image repository for the sync agent sidecar. |
| `agentImage.tag` | string | `""` | Agent image tag. Defaults to the chart's appVersion if empty. |
| `leaderElection.enabled` | bool | `true` | Enable leader election. Disable only for single-replica dev setups. |
| `resources.requests.cpu` | string | `10m` | Controller CPU request. |
| `resources.requests.memory` | string | `64Mi` | Controller memory request. |
| `resources.limits.cpu` | string | `500m` | Controller CPU limit. |
| `resources.limits.memory` | string | `128Mi` | Controller memory limit. |
| `nodeSelector` | object | `{}` | Node selector labels for the controller pod. |
| `tolerations` | list | `[]` | Tolerations for scheduling on tainted nodes. |
| `podAnnotations` | object | `{}` | Additional annotations to add to the controller pod. |
| `podLabels` | object | `{}` | Additional labels to add to the controller pod. |
| `affinity` | object | `{}` | Affinity rules for the controller pod. |
| `priorityClassName` | string | `""` | PriorityClass for the controller pod (e.g. `system-cluster-critical`). Protects the operator from eviction under node pressure. |
| `controller.logDevMode` | string | `"false"` | Enable zap Development mode for the controller logger. Development mode disables V-level filtering and uses console-friendly output. Set to `"true"` only for local development. |

### cert-manager

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `certManager.enabled` | bool | `true` | Create a self-signed Issuer and Certificate for webhook TLS. Requires cert-manager. |

### Metrics & Monitoring

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `metrics.enabled` | bool | `true` | Enable the metrics Service. |
| `metrics.service.type` | string | `ClusterIP` | Service type for the metrics endpoint. |
| `metrics.service.port` | int | `8443` | Port the metrics service listens on. |
| `serviceMonitor.enabled` | bool | `false` | Create a Prometheus ServiceMonitor for the controller. Requires prometheus-operator CRDs. |
| `serviceMonitor.labels` | object | `{}` | Additional labels for the ServiceMonitor (e.g. for Prometheus selector matching). |
| `serviceMonitor.interval` | string | `""` | Scrape interval. Falls back to the Prometheus default if empty. |
| `serviceMonitor.scrapeTimeout` | string | `""` | Scrape timeout. Falls back to the Prometheus default if empty. |
| `podMonitor.enabled` | bool | `false` | Create a PodMonitor for agent sidecar metrics. Requires prometheus-operator CRDs. |
| `podMonitor.labels` | object | `{}` | Additional labels for the PodMonitor. |
| `podMonitor.interval` | string | `""` | Scrape interval for agent metrics. |
| `podMonitor.scrapeTimeout` | string | `""` | Scrape timeout for agent metrics. |
| `prometheusRule.enabled` | bool | `false` | Create a PrometheusRule resource with default alerting rules. Requires prometheus-operator CRDs. |
| `prometheusRule.labels` | object | `{}` | Additional labels for the PrometheusRule (e.g. for Prometheus selector matching). |
| `prometheusRule.additionalRules` | list | `[]` | Extra alerting rules appended to the default set. |
| `grafanaDashboard.enabled` | bool | `false` | Create a ConfigMap containing Grafana dashboards (fleet overview + CR detail). Enable when using the k8s-sidecar for auto-discovery. |
| `grafanaDashboard.namespace` | string | `""` | Namespace for the dashboard ConfigMap. Defaults to the release namespace. Set to your Grafana namespace if the sidecar only watches a specific namespace. |
| `grafanaDashboard.labels` | object | `{}` | Additional labels for the dashboard ConfigMap. Override if your sidecar uses a label other than `grafana_dashboard: "1"`. |
| `grafanaDashboard.annotations` | object | `{}` | Annotations for the dashboard ConfigMap. |
| `networkPolicy.enabled` | bool | `false` | Create a NetworkPolicy restricting ingress to the metrics port. |

See the [Monitoring guide](../guides/monitoring.md) for details on available metrics and dashboard setup.

### Sidecar Injection Webhook

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `webhook.enabled` | bool | `true` | Enable the MutatingWebhookConfiguration. |
| `webhook.port` | int | `9443` | Webhook server port on the controller container. |
| `webhook.namespaceSelector.requireLabel` | bool | `false` | Require `stoker.io/injection=enabled` label on namespaces for injection. When false, injection works in all namespaces except `kube-system` and `kube-node-lease`. |

The webhook injects the agent sidecar into pods with annotation `stoker.io/inject: "true"`. By default, injection works in all namespaces except `kube-system` and `kube-node-lease`. Set `webhook.namespaceSelector.requireLabel=true` to require the `stoker.io/injection=enabled` namespace label.

### Agent RBAC

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `rbac.autoBindAgent.enabled` | bool | `true` | Automatically create RoleBindings for the agent sidecar in namespaces where GatewaySync CRs exist. The controller discovers ServiceAccounts from gateway pods and binds them to the `stoker-agent` ClusterRole. Disable for environments that manage RBAC externally. |

### Push Receiver (Webhook)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `webhookReceiver.enabled` | bool | `false` | Enable the webhook receiver HTTP server and its Service. When disabled, the controller does not start the receiver. |
| `webhookReceiver.port` | int | `9444` | Port for the inbound git webhook receiver (when enabled). |
| `webhookReceiver.hmac.secret` | string | `""` | HMAC secret value for `X-Hub-Signature-256` validation. Ignored if `secretRef` is set. |
| `webhookReceiver.hmac.secretRef.name` | string | `""` | Name of an existing Secret containing the HMAC key. |
| `webhookReceiver.hmac.secretRef.key` | string | `webhook-secret` | Key within the HMAC Secret. |
| `webhookReceiver.token.secret` | string | `""` | Static bearer token for `Authorization: Bearer` validation. Ignored if `secretRef` is set. |
| `webhookReceiver.token.secretRef.name` | string | `""` | Name of an existing Secret containing the bearer token. |
| `webhookReceiver.token.secretRef.key` | string | `webhook-token` | Key within the token Secret. |
| `webhookReceiver.ingress.enabled` | bool | `false` | Create an Ingress resource for the webhook receiver. |
| `webhookReceiver.ingress.ingressClassName` | string | `""` | Ingress class name (e.g. `nginx`, `traefik`, `alb`). Uses cluster default when empty. |
| `webhookReceiver.ingress.annotations` | object | `{}` | Annotations for the Ingress resource (ingress controller config, cert-manager, etc.). |
| `webhookReceiver.ingress.hosts` | list | `[]` | List of `{host, paths[]}` entries. Each path requires `path` and `pathType`. |
| `webhookReceiver.ingress.tls` | list | `[]` | TLS configuration: list of `{secretName, hosts[]}` entries. |

The push receiver accepts `POST /webhook/{namespace}/{crName}` and auto-detects payload format from GitHub releases, ArgoCD notifications, Kargo promotions, or generic `{"ref": "..."}` bodies. If both HMAC and bearer token are configured, either method can authorize a request.

:::warning
When enabled without any auth, any client that can reach the endpoint can trigger a reconcile. Configure `hmac` (GitHub-style) or `token` (bearer token, for Kargo and other CI/CD systems) for production use.
:::
