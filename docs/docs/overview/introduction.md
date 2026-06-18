---
sidebar_position: 1
title: Introduction
description: What is Stoker and why it exists.
---

# Introduction

Stoker is a Kubernetes operator that continuously syncs Ignition SCADA gateway configuration from Git. Declare your desired gateway state (projects, themes, config files) in a Git repository, and Stoker keeps every gateway in sync automatically.

## The problem

Managing Ignition gateway configuration across multiple environments is manual and error-prone. Configuration changes made through the Designer get lost, drift between gateways goes unnoticed, and there's no audit trail of what changed or when. As gateway counts grow, this approach doesn't scale.

## How Stoker solves it

Stoker brings GitOps to Ignition. You commit configuration to a Git repository and create a `GatewaySync` custom resource that describes what to sync and where. Stoker's controller resolves the Git ref, discovers gateway pods by annotation, and injects an agent sidecar that clones the repo and delivers files to each gateway. When the agent finishes syncing, it calls the Ignition scan API so the gateway picks up changes without a restart.

## Key features

- **Git-driven sync:** branch, tag, or commit SHA as the source of truth
- **Multi-gateway profiles:** one CR can serve many gateways using template variables (`{{.GatewayName}}`, `{{.Labels.site}}`)
- **Automatic sidecar injection:** a mutating webhook injects the sync agent with zero manual container config
- **Webhook-driven sync:** trigger instant syncs from GitHub releases, ArgoCD, Kargo, or any system that can POST JSON
- **Dry-run mode:** preview file changes in a status ConfigMap before touching the live directory
- **Designer session awareness:** proceed, wait, or abort when designers are connected
- **No shared storage:** controller and agent communicate entirely via ConfigMaps

## How it works

```
┌─────────────┐     ┌──────────────┐     ┌──────────────────┐
│  Git Repo   │◄────│  Controller  │────►│  Metadata CM     │
│  (ls-remote)│     │  (reconcile) │     │  (ref, commit,   │
└─────────────┘     └──────┬───────┘     │   auth, mappings)│
                           │             └────────┬─────────┘
                    discovers                     │
                    gateways                      │ reads
                           │             ┌────────▼─────────┐
                    ┌──────▼───────┐     │   Agent Sidecar  │
                    │ Gateway Pods │◄────│   (clone, sync,  │
                    │ (annotated)  │     │    scan API)     │
                    └──────────────┘     └────────┬─────────┘
                                                  │ writes
                                         ┌────────▼─────────┐
                                         │   Status CM      │
                                         │   (sync result)  │
                                         └──────────────────┘
```

1. The **controller** watches `GatewaySync` CRs, resolves Git refs via `ls-remote` (no clone), and discovers gateway pods by annotation
2. A **mutating webhook** injects the agent as a native sidecar when annotated pods are created
3. The **agent** reads the metadata ConfigMap, clones the repo, syncs files to the gateway's data directory, and calls the Ignition scan API to reload

## Next steps

- [Architecture](./architecture.md): deep dive into the controller, webhook, and agent
- [Quickstart](../quickstart.md): get a gateway syncing from Git in 5 steps
