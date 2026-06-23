<p align="center">
  <img src="docs/static/img/logo.png" alt="Stoker logo" width="180" />
</p>

# Stoker

<p align="center">
  <a href="https://github.com/knorrlabs/stoker-operator/actions/workflows/lint.yml"><img src="https://github.com/knorrlabs/stoker-operator/actions/workflows/lint.yml/badge.svg" alt="Lint"></a>
  <a href="https://github.com/knorrlabs/stoker-operator/actions/workflows/unit-test.yml"><img src="https://github.com/knorrlabs/stoker-operator/actions/workflows/unit-test.yml/badge.svg" alt="Test"></a>
  <a href="https://github.com/knorrlabs/stoker-operator/releases/latest"><img src="https://img.shields.io/github/v/release/knorrlabs/stoker-operator" alt="Release"></a>
  <a href="https://github.com/knorrlabs/stoker-operator/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="License: MIT"></a>
  <a href="https://knorrlabs.github.io/stoker-operator/"><img src="https://img.shields.io/badge/docs-knorrlabs.github.io-blue" alt="Docs"></a>
  <a href="https://goreportcard.com/report/github.com/knorrlabs/stoker-operator"><img src="https://goreportcard.com/badge/github.com/knorrlabs/stoker-operator" alt="Go Report Card"></a>
</p>

> **stok·er** /ˈstōkər/ — *a person who tends the fire in a furnace, feeding it fuel to keep it burning.*

Stoker tends your Ignition gateways, continuously feeding them configuration from Git to keep them running in the desired state.

## Features

- **Git-driven configuration sync** — gateway projects, tags, and resources managed in Git
- **Multi-gateway support** — manage any number of gateways from a single repository with template variables (`{{.GatewayName}}`, `{{.Labels.key}}`, `{{.CRName}}`)
- **Profile mappings** — declarative source-to-destination file mappings with glob patterns and per-pod template routing
- **Content templating** — resolve `{{.GatewayName}}`, `{{.Vars.key}}`, and other variables inside file contents at sync time; no source file modification required
- **JSON patches** — surgically update specific JSON fields per gateway using sjson dot-notation paths, without modifying source files in git
- **Automatic sidecar injection** — MutatingWebhook injects the sync agent into annotated pods
- **Gateway discovery** — controller discovers annotated pods and aggregates sync status
- **Webhook receiver** — push-event-driven sync via `POST /webhook/{namespace}/{crName}`

## Quick Start

```bash
# Install cert-manager (required for webhook TLS)
# https://cert-manager.io/docs/installation/

# Install the operator
helm install stoker oci://ghcr.io/knorrlabs/charts/stoker-operator \
  -n stoker-system --create-namespace
```

For a complete walkthrough — from installing the operator to syncing projects to an Ignition gateway — see the **[Quickstart Guide](https://knorrlabs.github.io/stoker-operator/quickstart)**.

## Architecture

```mermaid
flowchart LR
    Git[(Git Repo)] --> GatewaySync

    subgraph cluster [Cluster]
        GatewaySync
        subgraph ns [Namespace]
            subgraph pod [Gateway Pod]
                Agent[Agent Sidecar] --> GW[Ignition Gateway]
            end
        end
    end

    GatewaySync --> Agent
```

## CRDs

| CRD | Short Name | Description |
| --- | --- | --- |
| [`GatewaySync`](https://knorrlabs.github.io/stoker-operator/reference/gatewaysync-cr) | `gs` | Defines the git repository, auth, polling, sync profiles, and gateway connection settings |

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for build commands, testing, and development workflow.

## License

This project is licensed under the [MIT License](LICENSE).
