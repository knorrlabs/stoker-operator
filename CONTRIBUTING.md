# Contributing

Thanks for your interest in Stoker! This guide covers the local development workflow.

## Prerequisites

- Go 1.25+
- Docker (for image builds and functional tests)
- [kind](https://kind.sigs.k8s.io/) (for functional tests)
- kubectl
- Helm 3 (for chart testing)

## Setup

```bash
git clone https://github.com/knorrlabs/stoker-operator.git
cd stoker-operator
make setup    # configures git hooks
```

`make setup` points git to `.githooks/`, which includes a **pre-commit hook** that runs golangci-lint on staged Go files.

## Development Loop

```bash
make build    # build controller + agent binaries
make test     # unit tests (envtest)
make lint     # golangci-lint v2
make run      # run controller locally against your kubeconfig
```

### Running a Single Test

```bash
go test ./internal/controller/ -run TestControllers -v
go test ./internal/syncengine/ -run TestBuildPlan -v
```

Unit tests use **Ginkgo/Gomega with envtest** — a real API server is bootstrapped with CRDs. If running tests from an IDE, run `make setup-envtest` first to download the required binaries.

### E2E Tests

```bash
make e2e                                                       # full suite: setup kind + run tests
make e2e-test                                                  # run all Chainsaw tests (cluster must exist)
make e2e-test-focus TEST=controller-core/08-public-repo-no-auth  # single test
```

Tests use [Chainsaw](https://kyverno.github.io/chainsaw/) and run against a kind cluster with an in-cluster git server. Each test gets its own namespace.

## Modifying CRD Types

After editing files in `api/v1alpha1/`:

```bash
make manifests    # regenerate CRDs, RBAC, webhook config
make generate     # regenerate DeepCopy methods
make helm-sync    # copy CRDs to Helm chart + verify RBAC parity
```

`make helm-sync` will fail if the RBAC rules in `config/rbac/role.yaml` have drifted from the Helm chart's `clusterrole.yaml`. Update both to keep them in sync.

## Code Style

- **Linter:** golangci-lint v2 (config in `.golangci.yml`). Key linters: `revive`, `staticcheck`, `govet`, `errcheck`, `misspell`.
- **Formatting:** `gofmt` and `goimports` are enforced by the linter.
- **Commit messages:** Short and descriptive, following the style in git history (e.g., `add sidecar injection webhook`, `fix reconcile storm on status patches`).

## Pull Requests

- Keep PRs focused — one feature or fix per PR.
- Ensure `make lint` and `make test` pass before opening.
- New CRD fields need `make manifests && make generate && make helm-sync`.
- E2E test coverage (Chainsaw) is encouraged for end-to-end behavior changes.

## Project Layout

```
api/v1alpha1/          # CRD type definitions (GatewaySync)
cmd/controller/        # Controller binary entrypoint
cmd/agent/             # Agent binary entrypoint
internal/controller/   # GatewaySync reconciler
internal/agent/        # Agent orchestration (K8s-aware sync loop)
internal/syncengine/   # File sync engine (K8s-unaware, Ignition-unaware)
internal/ignition/     # Ignition gateway API client
internal/git/          # Git operations (ls-remote, clone/fetch)
internal/webhook/      # Pod injection webhook + webhook receiver
pkg/types/             # Shared annotations, labels, status types
pkg/conditions/        # Condition type/reason constants
config/                # Kustomize manifests (CRDs, RBAC, webhook, manager)
charts/stoker-operator/# Helm chart
test/e2e/              # Chainsaw e2e tests (kind-based)
```
