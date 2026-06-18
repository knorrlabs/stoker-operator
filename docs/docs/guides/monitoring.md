---
sidebar_position: 6
title: Monitoring
description: Prometheus metrics and Grafana dashboards for Stoker.
---

# Monitoring

Both the controller and agent sidecar expose Prometheus metrics. This guide covers the available metrics, how to enable scraping, and the pre-built Grafana dashboards.

## Metrics overview

### Controller metrics

The controller exposes metrics on port 8443 (the same HTTPS endpoint used by controller-runtime). These are scraped via a `ServiceMonitor`.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stoker_controller_reconcile_duration_seconds` | Histogram | `name`, `namespace` | Time spent in each reconcile loop |
| `stoker_controller_reconcile_total` | Counter | `name`, `namespace`, `result` | Total reconciles by result (`success`, `requeue`, `error`) |
| `stoker_controller_ref_resolve_duration_seconds` | Histogram | `name`, `namespace` | Time to resolve a git ref via `ls-remote` |
| `stoker_controller_gateways_discovered` | Gauge | `name`, `namespace` | Number of gateway pods found for a CR |
| `stoker_controller_gateways_synced` | Gauge | `name`, `namespace` | Number of gateways reporting Synced status |
| `stoker_controller_cr_ready` | Gauge | `name`, `namespace` | Whether the CR is in Ready condition (1/0) |
| `stoker_controller_cr_info` | Gauge | `name`, `namespace`, `git_repo`, `git_ref`, `auth_type`, `polling_interval` | Info metric (always 1) for PromQL joins |
| `stoker_controller_cr_paused` | Gauge | `name`, `namespace` | Whether the CR is paused (1/0) |
| `stoker_controller_condition_status` | Gauge | `name`, `namespace`, `type` | Per-condition status (1=True, 0=False) |
| `stoker_controller_gateway_sync_status` | Gauge | `name`, `namespace`, `gateway` | Per-gateway sync state (0=Pending, 1=Synced, 2=Error, 3=MissingSidecar) |
| `stoker_controller_gateway_last_sync_timestamp_seconds` | Gauge | `name`, `namespace`, `gateway` | Unix timestamp of the last agent sync per gateway |
| `stoker_controller_gateways_missing_sidecar` | Gauge | `name`, `namespace` | Count of gateways without the stoker-agent sidecar |
| `stoker_controller_github_app_token_expiry` | Gauge | `name`, `namespace` | Unix timestamp when the cached GitHub App token expires |

### Agent metrics

Each agent sidecar exposes metrics on port 8083 via a standalone Prometheus registry. These are scraped via a `PodMonitor`.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stoker_agent_sync_duration_seconds` | Histogram | `profile` | File sync operation duration |
| `stoker_agent_sync_total` | Counter | `profile`, `result` | Total syncs by result (`success`, `error`) |
| `stoker_agent_files_changed` | Gauge | `profile` | Files changed in the last sync |
| `stoker_agent_files_added` | Gauge | `profile` | Files added in the last sync |
| `stoker_agent_files_modified` | Gauge | `profile` | Files modified in the last sync |
| `stoker_agent_files_deleted` | Gauge | `profile` | Files deleted in the last sync |
| `stoker_agent_git_fetch_duration_seconds` | Histogram | `operation` | Git clone/fetch duration (`clone` or `fetch`) |
| `stoker_agent_git_fetch_total` | Counter | `operation`, `result` | Total git operations by result |
| `stoker_agent_scan_duration_seconds` | Histogram | — | Ignition scan API call duration |
| `stoker_agent_scan_total` | Counter | `result` | Total scan calls by result |
| `stoker_agent_designer_sessions_blocked` | Gauge | — | Whether sync is blocked by designer sessions (1/0) |
| `stoker_agent_designer_sessions_active` | Gauge | — | Count of active Ignition Designer sessions |
| `stoker_agent_last_sync_timestamp_seconds` | Gauge | — | Unix timestamp of the last successful sync |
| `stoker_agent_last_sync_success` | Gauge | — | Whether the last sync succeeded (1/0) |
| `stoker_agent_sync_skipped_total` | Counter | `reason` | Skipped syncs by reason (`commit_unchanged`, `paused`, `profile_error`, `designer_blocked`, `backoff`) |
| `stoker_agent_gateway_startup_duration_seconds` | Histogram | — | Time from agent start to gateway becoming responsive |

## Enabling scraping

### ServiceMonitor (controller)

```yaml
serviceMonitor:
  enabled: true
```

This creates a `ServiceMonitor` targeting the controller's HTTPS metrics endpoint. The ServiceMonitor uses `honorLabels: true` so the metric's own `namespace` label (the CR namespace) is preserved rather than being overwritten with the controller pod's namespace.

If your Prometheus uses a label selector, add matching labels:

```yaml
serviceMonitor:
  enabled: true
  labels:
    release: kube-prometheus-stack
```

### PodMonitor (agent)

```yaml
podMonitor:
  enabled: true
```

This creates a `PodMonitor` that matches pods with the `stoker.io/inject: "true"` annotation across all namespaces. Since agent sidecars run in gateway pod namespaces (not `stoker-system`), the PodMonitor uses `namespaceSelector.any: true`.

## Grafana dashboards

Two pre-built dashboards are shipped in the Helm chart under `dashboards/`:

| Dashboard | File | Description |
|-----------|------|-------------|
| **Fleet Overview** | `stoker-fleet.json` | High-level health across all GatewaySync CRs: summary stats, per-CR status cards with drill-down links, CR info table, controller performance, agent averages, webhook rates |
| **GatewaySync Detail** | `stoker-detail.json` | Deep dive into a single CR: conditions, per-gateway status table, controller and agent performance, file breakdown, designer sessions |

The fleet dashboard links to the detail view; click any CR status card to drill down with the namespace and CR pre-populated.

### Auto-provisioning via sidecar

If your Grafana uses the [k8s-sidecar](https://github.com/kiwigrid/k8s-sidecar) (the default in kube-prometheus-stack), enable the dashboard ConfigMap:

```yaml
grafanaDashboard:
  enabled: true
```

The sidecar detects the labeled ConfigMap (`grafana_dashboard: "1"`) and provisions both dashboards automatically.

If the sidecar watches a specific namespace rather than all namespaces, set `grafanaDashboard.namespace` to your Grafana namespace:

```yaml
grafanaDashboard:
  enabled: true
  namespace: monitoring
```

### Manual import

For Grafana instances without the sidecar (standalone, Docker, Grafana Cloud), copy the JSON files from the chart and import them via **Dashboards > New > Import** in the Grafana UI. Both dashboards use a `$datasource` template variable so they work with any Prometheus data source.

### Dashboard variables

The **fleet dashboard** has two variables:
- `datasource`: Prometheus data source
- `namespace`: multi-select filter for CR namespaces (defaults to All)

The **detail dashboard** has five variables:
- `datasource`: Prometheus data source
- `namespace`: single CR namespace (from controller metrics)
- `cr`: single GatewaySync CR name
- `agent_namespace`: multi-select filter for agent pod namespaces (separate from CR namespace since agents run in gateway namespaces)
- `profile`: multi-select filter for sync profiles

:::tip
Controller metrics use `namespace` = the CR's namespace. Agent metrics use `namespace` = the gateway pod's namespace. These are typically different namespaces. The dashboards handle this with separate template variables.
:::

## Useful PromQL queries

**CRs not ready:**
```promql
stoker_controller_cr_ready == 0
```

**Slow reconciles (p95 > 1s):**
```promql
histogram_quantile(0.95, sum by (le, name) (rate(stoker_controller_reconcile_duration_seconds_bucket[5m]))) > 1
```

**Gateways with sync errors:**
```promql
stoker_controller_gateway_sync_status == 2
```

**Agent sync failures in the last hour:**
```promql
increase(stoker_agent_sync_total{result="error"}[1h]) > 0
```

**Git ref and repo for a CR (info gauge join):**
```promql
stoker_controller_cr_ready * on (name, namespace) group_left(git_repo, git_ref) stoker_controller_cr_info
```
