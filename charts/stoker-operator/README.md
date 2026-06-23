# stoker-operator

![Version: 0.7.0](https://img.shields.io/badge/Version-0.7.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.7.0](https://img.shields.io/badge/AppVersion-0.7.0-informational?style=flat-square)

Kubernetes operator that syncs Ignition gateway projects from a Git repository

**Homepage:** <https://github.com/knorrlabs/stoker-operator>

## Prerequisites

- Kubernetes >= 1.28
- Helm 3
- [cert-manager](https://cert-manager.io/) (when `certManager.enabled=true`)

## Installation

```bash
helm install stoker oci://ghcr.io/knorrlabs/charts/stoker-operator
```

See the post-install notes (`helm get notes <release>`) for next steps: creating
secrets and applying CRs. Agent RBAC is managed automatically by default.

## Architecture

The operator has two components:

- **Controller** — watches GatewaySync CRs, resolves git refs via `ls-remote`,
  and manages metadata ConfigMaps.
- **Agent sidecar** — injected into gateway pods via MutatingWebhook, clones the
  repo and syncs files to the Ignition data directory.

Two webhook-like features exist and are configured separately:

| Feature | Values key | Description |
|---------|------------|-------------|
| Sidecar injection | `webhook.*` | MutatingWebhook that injects the stoker-agent into annotated pods |
| Push receiver | `webhookReceiver.*` | HTTP endpoint that accepts GitHub/GitLab push events for immediate sync |

## Monitoring

Both the controller and agent sidecar expose Prometheus metrics. Enable
`serviceMonitor` and `podMonitor` to have prometheus-operator discover them
automatically.

### Grafana Dashboards

Two pre-built dashboards are included in `dashboards/`:

| Dashboard | File | Purpose |
|-----------|------|---------|
| **Fleet Overview** | `stoker-fleet.json` | High-level health across all GatewaySync CRs — summary stats, CR status cards, controller performance, agent averages, webhooks |
| **GatewaySync Detail** | `stoker-detail.json` | Deep dive into a single CR — conditions, per-gateway status table, controller and agent performance, file breakdown, designer sessions |

The fleet dashboard links to the detail view — click any CR card to drill down.

**Sidecar auto-discovery (kube-prometheus-stack)** — If your Grafana uses the
[k8s-sidecar](https://github.com/kiwigrid/k8s-sidecar) (the default in
kube-prometheus-stack), enable the dashboard ConfigMap:

```yaml
grafanaDashboard:
  enabled: true
```

The sidecar detects the labeled ConfigMap and provisions both dashboards
automatically. This is additive — it does not modify or remove any existing
dashboards. If the sidecar watches a specific namespace rather than all
namespaces, set `grafanaDashboard.namespace` to your Grafana namespace.

**Manual import** — For Grafana instances without the sidecar (standalone,
Docker, Grafana Cloud), copy the JSON files and import them via
**Dashboards > New > Import** in the Grafana UI. Both dashboards use a
`$datasource` template variable so they work with any Prometheus data source.

## Requirements

Kubernetes: `>= 1.28.0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for scheduling the controller pod. |
| agentImage | object | `{"repository":"ghcr.io/knorrlabs/stoker-agent","tag":""}` | Agent sidecar image injected into gateway pods by the webhook. |
| agentImage.repository | string | `"ghcr.io/knorrlabs/stoker-agent"` | Image repository for the sync agent sidecar. |
| agentImage.tag | string | `""` | Image tag. Defaults to the chart's appVersion if empty. |
| certManager | object | `{"enabled":true}` | cert-manager integration for webhook TLS certificates. Requires cert-manager to be installed in the cluster. |
| certManager.enabled | bool | `true` | Create a self-signed Issuer and Certificate for webhook TLS. Requires cert-manager to be installed in the cluster. |
| controller | object | `{"logDevMode":"false"}` | Controller configuration. |
| controller.logDevMode | string | `"false"` | Enable zap Development mode for the controller logger. Development mode disables V-level filtering and uses console-friendly output. Set to "true" only for local development. |
| fullnameOverride | string | `""` | Override the full release name used in resource names. |
| grafanaDashboard | object | `{"annotations":{},"enabled":false,"labels":{},"namespace":""}` | Grafana dashboard provisioning via sidecar auto-discovery. Creates a ConfigMap labeled `grafana_dashboard: "1"` that the Grafana sidecar (k8s-sidecar) detects and provisions automatically. This is the standard pattern used by kube-prometheus-stack and does not affect existing dashboards. If your Grafana instance does not use the sidecar, you can import the dashboard JSON manually from charts/stoker-operator/dashboards/stoker-overview.json. |
| grafanaDashboard.annotations | object | `{}` | Annotations for the dashboard ConfigMap. |
| grafanaDashboard.enabled | bool | `false` | Create a ConfigMap containing the Stoker Grafana dashboard. Enable when your Grafana uses the k8s-sidecar for dashboard auto-discovery (default in kube-prometheus-stack). |
| grafanaDashboard.labels | object | `{}` | Additional labels for the dashboard ConfigMap. Override if your sidecar uses a label other than `grafana_dashboard: "1"`. |
| grafanaDashboard.namespace | string | `""` | Namespace for the dashboard ConfigMap. Defaults to the release namespace. Set this to your Grafana namespace if the sidecar only watches a specific namespace. |
| image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/knorrlabs/stoker-operator","tag":""}` | Controller container image configuration. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy (Always, IfNotPresent, Never). |
| image.repository | string | `"ghcr.io/knorrlabs/stoker-operator"` | Image repository for the controller manager. |
| image.tag | string | `""` | Image tag. Defaults to the chart's appVersion if empty. |
| imagePullSecrets | list | `[]` | Credentials for private container registries. Example:   imagePullSecrets:     - name: my-registry-secret |
| leaderElection | object | `{"enabled":true}` | Leader election prevents multiple controller instances from reconciling simultaneously. Disable only for single-replica development setups. |
| leaderElection.enabled | bool | `true` | Enable leader election for controller manager. |
| metrics | object | `{"enabled":true,"service":{"port":8443,"type":"ClusterIP"}}` | Metrics endpoint configuration. The controller exposes Prometheus metrics over HTTPS on the metrics service port. |
| metrics.enabled | bool | `true` | Enable the metrics Service. |
| metrics.service.port | int | `8443` | Port the metrics service listens on. |
| metrics.service.type | string | `"ClusterIP"` | Service type for the metrics endpoint. |
| nameOverride | string | `""` | Override the chart name used in resource names. |
| networkPolicy | object | `{"enabled":false}` | NetworkPolicy restricts ingress to the metrics port. Only allows traffic from namespaces labeled `metrics: enabled`. |
| networkPolicy.enabled | bool | `false` | Create a NetworkPolicy for the controller. |
| nodeSelector | object | `{}` | Node selector labels for scheduling the controller pod. Example:   nodeSelector:     kubernetes.io/os: linux |
| podAnnotations | object | `{}` | Additional annotations to add to the controller pod. |
| podLabels | object | `{}` | Additional labels to add to the controller pod. |
| podMonitor | object | `{"enabled":false,"interval":"","labels":{},"scrapeTimeout":""}` | PodMonitor for scraping agent sidecar metrics across all namespaces. Requires the prometheus-operator CRDs to be installed in the cluster. |
| podMonitor.enabled | bool | `false` | Create a PodMonitor resource for agent sidecars. |
| podMonitor.interval | string | `""` | Scrape interval. Falls back to the Prometheus default if empty. |
| podMonitor.labels | object | `{}` | Additional labels for the PodMonitor (e.g. for Prometheus selector matching). |
| podMonitor.scrapeTimeout | string | `""` | Scrape timeout. Falls back to the Prometheus default if empty. |
| priorityClassName | string | `""` | PriorityClass for the controller pod, e.g. system-cluster-critical. Protects the operator from eviction under node pressure. |
| prometheusRule | object | `{"additionalRules":[],"enabled":false,"labels":{}}` | PrometheusRule for Stoker alerting rules. Requires the prometheus-operator CRDs to be installed in the cluster. |
| prometheusRule.additionalRules | list | `[]` | Additional alerting rules appended to the default set. |
| prometheusRule.enabled | bool | `false` | Create a PrometheusRule resource with default alerts. |
| prometheusRule.labels | object | `{}` | Additional labels for the PrometheusRule. |
| rbac | object | `{"autoBindAgent":{"enabled":true}}` | RBAC configuration for the agent sidecar. |
| rbac.autoBindAgent.enabled | bool | `true` | Automatically create RoleBindings for the agent sidecar in namespaces where GatewaySync CRs exist. The controller discovers ServiceAccounts from gateway pods and binds only those SAs to the stoker-agent ClusterRole. Disable for environments that manage RBAC externally (e.g., GitOps-managed RBAC). |
| replicaCount | int | `1` | Number of controller replicas. Only one replica holds the leader lock at a time; additional replicas provide fast failover. |
| resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | CPU and memory resource requests/limits for the controller container. The controller runs git ls-remote (no clone) and watches CRs, so resource requirements are modest. |
| serviceMonitor | object | `{"enabled":false,"interval":"","labels":{},"scrapeTimeout":""}` | Prometheus ServiceMonitor for automatic scrape target discovery. Requires the prometheus-operator CRDs to be installed in the cluster. |
| serviceMonitor.enabled | bool | `false` | Create a ServiceMonitor resource. |
| serviceMonitor.interval | string | `""` | Scrape interval. Falls back to the Prometheus default if empty. |
| serviceMonitor.labels | object | `{}` | Additional labels for the ServiceMonitor (e.g. for Prometheus selector matching). |
| serviceMonitor.scrapeTimeout | string | `""` | Scrape timeout. Falls back to the Prometheus default if empty. |
| tolerations | list | `[]` | Tolerations for scheduling the controller pod on tainted nodes. |
| webhook | object | `{"enabled":true,"namespaceSelector":{"requireLabel":false},"port":9443}` | Mutating webhook for sidecar injection. When enabled, pods with annotation `stoker.io/inject: "true"` get the stoker-agent sidecar injected automatically. By default, injection works in all namespaces except kube-system and kube-node-lease. |
| webhook.enabled | bool | `true` | Enable the MutatingWebhookConfiguration and webhook Service. |
| webhook.namespaceSelector.requireLabel | bool | `false` | Require the stoker.io/injection=enabled label on namespaces for sidecar injection. When false (default), the webhook intercepts pod creates in all namespaces except kube-system and kube-node-lease. Enable for regulated environments that require explicit namespace opt-in. |
| webhook.port | int | `9443` | Webhook server port on the controller container. |
| webhookReceiver | object | `{"enabled":false,"hmac":{"secret":"","secretRef":{"key":"webhook-secret","name":""}},"ingress":{"annotations":{},"enabled":false,"hosts":[],"ingressClassName":"","tls":[]},"port":9444,"token":{"secret":"","secretRef":{"key":"webhook-token","name":""}}}` | Git webhook receiver for push-event-driven sync. Disabled by default — enable when you want push-event-driven syncs. When disabled, the controller does not start the HTTP receiver server. When enabled without HMAC, any network client that can reach the Service can trigger a reconcile. Configure hmac for production use. |
| webhookReceiver.enabled | bool | `false` | Enable the webhook receiver HTTP server and its Service. |
| webhookReceiver.hmac | object | `{"secret":"","secretRef":{"key":"webhook-secret","name":""}}` | HMAC secret for validating webhook signatures (X-Hub-Signature-256). Provide either a literal value or a reference to an existing Secret. |
| webhookReceiver.hmac.secret | string | `""` | HMAC secret value. Ignored if secretRef is set. |
| webhookReceiver.hmac.secretRef | object | `{"key":"webhook-secret","name":""}` | Reference to an existing Secret containing the HMAC key. |
| webhookReceiver.hmac.secretRef.key | string | `"webhook-secret"` | Key within the Secret. |
| webhookReceiver.hmac.secretRef.name | string | `""` | Name of the Secret. |
| webhookReceiver.ingress | object | `{"annotations":{},"enabled":false,"hosts":[],"ingressClassName":"","tls":[]}` | Ingress for the webhook receiver. Exposes the receiver outside the cluster for push-event-driven syncs from Kargo, GitHub, or other external systems. |
| webhookReceiver.ingress.annotations | object | `{}` | Annotations for the Ingress resource. Use to configure your ingress controller or attach a cert-manager certificate. Example (AWS ALB internal):   kubernetes.io/ingress.class: alb   alb.ingress.kubernetes.io/scheme: internal   alb.ingress.kubernetes.io/group.name: my-group   alb.ingress.kubernetes.io/target-type: ip Example (nginx + cert-manager):   cert-manager.io/cluster-issuer: letsencrypt-prod   nginx.ingress.kubernetes.io/ssl-redirect: "true" |
| webhookReceiver.ingress.enabled | bool | `false` | Create an Ingress resource for the webhook receiver. |
| webhookReceiver.ingress.hosts | list | `[]` | Hosts and paths to expose. At least one host is required when enabled. Example:   - host: stoker.example.com     paths:       - path: /webhook         pathType: Prefix |
| webhookReceiver.ingress.ingressClassName | string | `""` | Ingress class name (e.g. "nginx", "traefik", "alb"). When empty, the cluster default ingress class is used. |
| webhookReceiver.ingress.tls | list | `[]` | TLS configuration. Omit to rely on ingress controller defaults or cert-manager annotations. Example:   - secretName: stoker-webhook-tls     hosts:       - stoker.example.com |
| webhookReceiver.port | int | `9444` | Port for the inbound git webhook receiver. |
| webhookReceiver.token | object | `{"secret":"","secretRef":{"key":"webhook-token","name":""}}` | Static bearer token for authenticating webhook requests. Kargo and other callers that cannot compute HMAC signatures should use this. If both token and hmac are configured, either method can authorize a request. |
| webhookReceiver.token.secret | string | `""` | Bearer token value. Ignored if secretRef is set. |
| webhookReceiver.token.secretRef | object | `{"key":"webhook-token","name":""}` | Reference to an existing Secret containing the bearer token. |
| webhookReceiver.token.secretRef.key | string | `"webhook-token"` | Key within the Secret. |
| webhookReceiver.token.secretRef.name | string | `""` | Name of the Secret. |

