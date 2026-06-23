# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Stoker is a Kubernetes operator that continuously syncs Ignition SCADA gateway configuration from Git. It uses a controller + agent sidecar architecture where the controller resolves git refs via `ls-remote` (no clone) and agents independently clone repos to sync files to gateway pods.

**Module:** `github.com/knorrlabs/stoker-operator`
**CRD:** `GatewaySync` (`stoker.io/v1alpha1`, short name `gs`)

## Build & Dev Commands

```bash
make build              # Build controller + agent binaries to bin/
make test               # Unit tests (envtest-based, requires make setup-envtest first)
make lint               # golangci-lint v2 (config in .golangci.yml)
make lint-fix           # Lint with auto-fix
make manifests          # Regenerate CRDs, RBAC, webhook config via controller-gen
make generate           # Regenerate DeepCopy methods
make helm-sync          # Copy CRDs to Helm chart + verify RBAC drift
make run                # Run controller locally against current kubeconfig
make e2e                # Full e2e: setup kind cluster + run Chainsaw tests
make e2e-setup          # Set up kind cluster and deploy operator
make e2e-test           # Run all Chainsaw e2e tests (cluster must exist)
make e2e-test-focus TEST=controller-core/08-public-repo-no-auth  # Run single test
make e2e-teardown       # Delete the e2e kind cluster
make setup              # Configure git hooks (.githooks/pre-commit runs lint)
```

**Run a single test:**
```bash
go test ./internal/controller/ -run TestControllers -v
go test ./internal/syncengine/ -run TestBuildPlan -v
```

**Unit tests use Ginkgo/Gomega with envtest.** The suite bootstraps a real API server with CRDs from `config/crd/bases/`. Run `make setup-envtest` before running tests from an IDE.

**E2E tests use [Chainsaw](https://kyverno.github.io/chainsaw/) (by Kyverno).** Tests live in `test/e2e/` and run against a real kind cluster with the operator deployed via Helm. Each test gets its own namespace. An in-cluster git server provides repos without external dependencies. Tests cover: controller reconciliation, profile validation, gateway discovery, webhook injection, and webhook receiver.

## Architecture

### Two Binaries, Two Images

| Binary | Entry | Image | Purpose |
|--------|-------|-------|---------|
| controller | `cmd/controller/main.go` | `ghcr.io/knorrlabs/stoker-operator` | Reconciles CRs, resolves git refs, discovers gateways |
| agent | `cmd/agent/main.go` | `ghcr.io/knorrlabs/stoker-agent` | Runs as native sidecar, clones repo, syncs files to gateway |

### Controller ↔ Agent Communication

No shared PVC. Communication is entirely via ConfigMaps:
- `stoker-metadata-{crName}` — controller writes commit, ref, git URL, auth type, paused flag, resolved profiles JSON, gateway port/TLS
- `stoker-status-{crName}` — agent writes sync status per gateway

(`stoker-changes-{crName}` is a legacy name still removed during finalizer cleanup; nothing writes it.)

### Agent 3-Layer Architecture

```
internal/syncengine  →  K8s-unaware, Ignition-unaware. Takes a plan, copies files.
internal/agent       →  K8s-aware (reads ConfigMaps, writes status). Orchestrates sync loop.
internal/ignition    →  Ignition API client (scan, health check, designer sessions).
```

The sync engine builds staging at `/ignition-data/.sync-staging/`, then merges to the live directory. Only managed paths are cleaned (orphan cleanup scoped to mapped directories).

### Sidecar Injection

Mutating webhook at `/mutate-v1-pod` injects the agent as a native sidecar (`initContainer` with `restartPolicy: Always`, K8s 1.28+). Injection requires:
1. Namespace label: `stoker.io/injection=enabled`
2. Pod annotation: `stoker.io/inject: "true"`

Agent image is resolved 2-tier: CR `spec.agent.image` > env `DEFAULT_AGENT_IMAGE` (the pod-annotation tier was removed in v0.6.0).

### Webhook Receiver

HTTP server on port 9444 (`POST /webhook/{namespace}/{crName}`). Auto-detects payload format from GitHub releases, ArgoCD notifications, Kargo promotions, or generic `{"ref": "..."}`. HMAC validation via `X-Hub-Signature-256` header when `WEBHOOK_HMAC_SECRET` is set.

## Key Conventions

- **Annotation prefix:** `stoker.io/` (defined in `pkg/types/annotations.go`)
- **Finalizer:** `stoker.io/finalizer`
- **Status patches:** Uses `client.MergeFrom()` to avoid resourceVersion conflicts
- **Predicate filter:** Controller watches GatewaySync CRs with a custom predicate that passes on generation change OR annotation change (for webhook-triggered reconciles)
- **Git auth:** Controller resolves secrets for `ls-remote`; agent reads from hardcoded mount paths `/etc/stoker/git-credentials` and `/etc/stoker/api-key`. For GitHub App auth, the controller exchanges the PEM for an installation token, caches it, and writes it to the Secret `stoker-github-token-{crName}`, which the webhook mounts into the agent at `/etc/stoker/git-token/token` (PEM never mounted into agent pods).

## Code Generation Workflow

After modifying types in `api/v1alpha1/`:
```bash
make manifests    # Regenerate CRDs + RBAC
make generate     # Regenerate DeepCopy
make helm-sync    # Sync CRDs to Helm chart, verify RBAC parity
```

Clean `.claude/worktrees/` before running `controller-gen` — stale Go files in worktrees can cause generation errors.

## Helm Chart

Located at `charts/stoker-operator/`. CRDs live in `charts/stoker-operator/crds/` and must be kept in sync with `config/crd/bases/` via `make helm-sync`. Chart README is generated by `helm-docs` (`make helm-docs`).
