# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Security

- **Sync engine rejects symlink sources** — `copyFile` now `Lstat`s the source before opening it and returns an error when the source is a symlink. Previously a commit in a synced repo could include a symlink (e.g. `config/creds.txt -> /etc/stoker/git-credentials`) and a profile mapping that referenced it, causing the agent to follow the symlink and copy mounted credentials into the gateway data directory.

### Removed

- **`stoker.io/agent-image` pod annotation** — the pod-annotation tier of agent image resolution is removed. The annotation let any pod author in an injection-enabled namespace specify an arbitrary container image, which the mutating webhook would then inject as a sidecar — bypassing cluster image-policy admission controllers. Agent image is now resolved only from `spec.agent.image` on the GatewaySync CR or the `DEFAULT_AGENT_IMAGE` environment variable.

  **Upgrade impact:** pods that set `stoker.io/agent-image` will continue to be admitted without error, but the annotation is now ignored. The injected agent will use the CR spec or env default instead. If you relied on the override (debug builds, per-pod canary images), move the image into the CR's `spec.agent.image` field or set `DEFAULT_AGENT_IMAGE` on the controller deployment.

## [v0.5.2] - 2026-05-11

### Changed

- Upgraded `sigs.k8s.io/controller-runtime` to `v0.24.0` for Kubernetes v1.36 compatibility — `v0.23.1` was incompatible with `k8s.io/client-go v0.36.0` due to a missing `HasSyncedChecker` implementation in the multi-namespace cache (#112)
- Upgraded Kubernetes libraries to v1.36 (`k8s.io/apiextensions-apiserver`, `k8s.io/apiserver`, `k8s.io/component-base`) (#109, #112)
- Upgraded Go toolchain to v1.26 (#108)
- Dependency updates: `go-git/v5` v5.19.0, `ginkgo/v2` v2.28.3, `gomega` v1.40.0, `golang.org/x/crypto` v0.51.0 (#110, #111, #113, #114)

## [v0.5.1] - 2026-03-05

### Fixed

- **Orphan cleanup skips nested managed roots** — `cleanOrphans` and `computeDryRunDiff` returned `filepath.SkipDir` for any directory not directly under a managed root (e.g. `config/`), preventing traversal into nested destinations like `config/resources/dev/ignition/audit-profile`; orphan files and directories inside them were never deleted (`deleted: 0`); fix adds `isAncestorOfManagedRoot` to allow traversal through intermediate directories, and bubbles up empty directory removal after orphan file deletion (#99, #100)

## [v0.5.0] - 2026-03-01

### Added

- **SSH host key verification** — optional `knownHosts` field on `spec.git.auth.sshKey` references a Secret containing SSH `known_hosts` data; when configured, both the controller (`ls-remote`) and agent (clone/fetch) use strict host key checking instead of `InsecureIgnoreHostKey`; a new `SSHHostKeyVerification` status condition warns when SSH auth is used without `knownHosts`
- **Exponential backoff** — transient git and secret validation errors now back off exponentially (30s → 60s → 120s → 240s → 5min cap) instead of retrying every 30s; backoff resets on success or webhook-triggered reconcile; applies to both controller ref resolution and agent sync loop
- **Graceful shutdown** — agent catches SIGTERM, marks readiness probe as failing immediately, waits up to 15s for any in-flight file sync to complete before exiting; prevents partial file writes when pods are terminated

## [v0.4.10] - 2026-02-28

### Fixed

- **`Ref` column now shows resolved ref** — the `kubectl get gs` `Ref` printer column previously read `spec.git.ref` (written by ArgoCD, lags up to 3 minutes after a promotion); changed to `status.lastSyncRef` so the column reflects the ref the controller actually resolved and synced, updating in seconds after a webhook trigger

## [v0.4.9] - 2026-03-01

### Fixed

- **Stale `requested-ref` annotation** — the controller now clears the `stoker.io/requested-ref` annotation once `spec.git.ref` catches up to the value the webhook set (with `v`-prefix normalization so `v2.2.3` matches `2.2.3`); previously the annotation was never removed, permanently overriding `spec.git.ref` and leaving the controller pinned to an old ref whenever a subsequent promotion's webhook failed silently
- **Idempotent webhook returns 202** — the webhook receiver now returns `202 Accepted` (not `200 OK`) when the requested ref is already set; clients using `successExpression: response.status == 202` (e.g. Kargo) no longer treat idempotent calls as failures

## [v0.4.8] - 2026-03-01

### Added

- **Bearer token auth for webhook receiver** — `webhookReceiver.token` Helm block (parallel to `hmac`); controller reads `WEBHOOK_BEARER_TOKEN` env var and validates `Authorization: Bearer <token>` header; any HTTP client that can set headers can authenticate; if both HMAC and token are configured, either method can authorize a request

## [v0.4.7] - 2026-03-01

### Added

- **Webhook receiver Ingress** — `webhookReceiver.ingress` block in Helm values; creates an Ingress resource exposing the receiver outside the cluster for push-event-driven syncs from Kargo, GitHub, or other external systems; generic annotations/hosts/tls structure works with any ingress controller (ALB, nginx, Traefik)

## [v0.4.6] - 2026-02-28

### Fixed

- **Annotated tag resolution** — `LsRemote` now returns the peeled commit hash (`^{}`) instead of the tag object hash for annotated tags; previously the controller and agent disagreed on the resolved commit, causing an infinite re-sync loop every 30s
- **Root-level file mapping orphan wipe** — `computeManagedRoots` used `filepath.Dir(dst)` for file-type mappings, which collapses to `"."` for root-level destinations (e.g. `.versions.json`); `isUnderManagedRoot` then matched every path, causing orphan cleanup to delete all Ignition runtime files on every sync (internal database, `local` resource collection manifest, OPC-UA keystores) and fault the gateway (#89)

## [v0.4.5] - 2026-02-28

### Fixed

- **`safe.directory` ownership check** — git 2.35.2+ refuses to operate on a repository whose root directory is not owned by the current user; agent pods inherit the Ignition pod's `runAsUser` (e.g. `2003`) but the `/repo` emptyDir mount point is created by Kubernetes as root; fix writes a minimal `.gitconfig` to `/tmp` (which is `$HOME`) with `[safe] directory = *` before any git operation (#87)

## [v0.4.4] - 2026-02-28

### Fixed

- **SSH auth on pods with custom `runAsUser`** — OpenSSH refuses to run when the current UID has no `/etc/passwd` entry; agent pods inherit the Ignition pod's `runAsUser` (e.g. `2003`) which doesn't exist in Alpine's passwd; fix uses an SSH wrapper script in `/tmp` that leverages `nss_wrapper` to inject a minimal passwd entry for the current UID before invoking ssh (#86)

### Changed

- **Agent base image** — added `nss_wrapper` package to `alpine:3.21` image for the SSH passwd fix (#86)

## [v0.4.3] - 2026-02-28

### Changed

- **Native git for agent clone/fetch** — replaced go-git's `CloneOrFetch` with `exec.Command("git", ...)` using `--depth=1` shallow clones; eliminates the OOM kills caused by go-git loading entire pack files into memory during initial clone of large repositories (#85)
- **Agent base image** — replaced `distroless/static-debian12:nonroot` with `alpine:3.21 + git + openssh-client` to provide the native git binary; existing security context (`readOnlyRootFilesystem`, `drop ALL`, `seccompProfile: RuntimeDefault`) is unchanged (#85)

### Fixed

- Agent pod OOMKills on large repositories during initial clone (#85)

### Added

- Writable `/tmp` emptyDir injected into agent sidecar pods; native git requires scratch space for lock files and `known_hosts` under `readOnlyRootFilesystem: true` (#85)

## [v0.4.2] - 2026-02-28

### Added

- **`podAnnotations` and `podLabels`** — Helm values for adding arbitrary annotations and labels to the controller pod (#84)

### Fixed

- **Agent startup probe timeout** — increased `failureThreshold` from 30 → 150 (60s → 5min) to accommodate initial clone of large repositories before the first sync completes (#84)

## [v0.4.1] - 2026-02-28

### Added

- **`{{.PodOrdinal}}` template variable** — StatefulSet replica index sourced from the `apps.kubernetes.io/pod-index` label (K8s 1.27+) with automatic fallback to parsing the trailing integer from the pod name; enables `"{{.GatewayName}}-{{.PodOrdinal}}"` patterns for exact parity with existing systemName conventions (#83)

### Changed

- **Var key validation** — `spec.sync.defaults.vars` and `spec.sync.profiles.*.vars` keys are now validated as Go identifiers (letters, digits, underscores; no dashes) at reconcile time; invalid keys set `ProfilesValid=False` with a clear error message instead of silently failing with a cryptic template parse error at sync time (#83)

## [v0.4.0] - 2026-02-27

### Added

- **Content templating** (`template: true`) — resolve Go template variables (`{{.GatewayName}}`, `{{.PodName}}`, `{{.Vars.key}}`, etc.) inside file **contents** at sync time, without modifying source files in git; binary files (null bytes) are rejected with a clear error (#82)
- **`vars` in `spec.sync.defaults`** — define default template variables shared across all profiles; profile `vars` override per-key (#82)
- **`{{.PodName}}` in TemplateContext** — enables unique system names for StatefulSet replicas with ordinal-suffix pod names (#82)
- **JSON path patches** — per-mapping `patches` blocks that set specific JSON fields at sync time using sjson dot-notation paths; patch values support Go template syntax; `file` field supports doublestar globs; type inference from filesystem when `type` field is omitted (#82)

### Changed

- **GitHub App tokens moved to dedicated Secret** — installation tokens are now written to `stoker-github-token-{crName}` (a controller-managed Secret) and mounted into agent pods; tokens are no longer stored in the metadata ConfigMap (#82)

### Fixed

- Agent now re-syncs when CR profiles change (new patches, vars, or mappings) even if the git commit has not changed; previously a profile change without a new commit was ignored until the next pod restart (#82)

## [v0.3.0] - 2026-02-25

### Breaking Changes

- **Renamed `gateway.apiKeySecretRef`** to `gateway.api.secretName` / `gateway.api.secretKey` — `secretKey` defaults to `"apiKey"` when omitted, reducing boilerplate (#79)
- Gateway port default changed from `8043` to `8088` and TLS default changed from `true` to `false`, matching Ignition Helm chart defaults (#76)
- Webhook receiver disabled by default — enable via `webhookReceiver.enabled: true` in Helm values (#76)

### Added

- **GitHub App authentication** — controller exchanges PEM for short-lived installation tokens with per-CR cache and 5-minute pre-expiry refresh; PEM never leaves controller namespace; supports GitHub Enterprise Server via `apiBaseURL` field (#76)

## [v0.2.0] - 2026-02-24

### Breaking Changes

- **Merged `SyncProfile` into `GatewaySync` CRD** — sync profiles are now embedded at `spec.sync.profiles` instead of a separate CRD; `spec.sync.defaults` provides inheritable baseline settings (#51)
- Removed `deploymentMode` field from sync profile spec (#48)
- Namespace injection label (`stoker.io/injection=enabled`) now optional, disabled by default — injection requires only the `stoker.io/inject: "true"` pod annotation (#64)

### Added

- **Automatic agent RBAC** — controller creates Role/RoleBinding for the agent ServiceAccount in each target namespace (#68)
- **Chainsaw e2e test suite** replacing shell functional tests with declarative Kyverno Chainsaw tests against a real kind cluster (#47, #50)
- **Documentation site** — Docusaurus-based docs with quickstart, guides (multi-gateway, webhook sync, git auth), and CRD reference (#41, #63, #67)

### Fixed

- Controller defers secret validation until after ref resolution, avoiding false errors on public repos (#58)
- Dry-run mode now reports `Synced` status on success instead of staying `Pending` (#59)
- Webhook writes discovered `cr-name` annotation back to pod spec (#60)
- Agent respects profile-level `syncPeriod` from metadata ConfigMap (#61)
- Suppress `NotFound` error log on CR deletion race (#62)

### Changed

- Quickstart: cert-manager moved to prerequisites, added example repo context (#71)
- Cleaned up CI workflow names and e2e trigger strategy (#66)
- Removed stale design docs, scripts, and assets (#70)

## [v0.1.2] - 2026-02-22

### Fixed

- Controller failed to match gateway status from ConfigMap when `stoker.io/gateway-name` annotation was unset, causing gateways to stay `Pending` indefinitely

## [v0.1.1] - 2026-02-22

### Fixed

- Webhook unconditionally mounted a `git-credentials` secret volume even when `spec.git.auth` was nil, causing pods using public repos to get stuck in Init

## [v0.1.0] - 2026-02-22

Initial release — controller + agent sidecar for Git-driven Ignition gateway configuration sync.

### Added

- **GatewaySync CRD** (`stoker.io/v1alpha1`) with git ref resolution via `ls-remote`, polling configuration, gateway connection settings, and embedded sync profiles with declarative source-to-destination file mappings, glob patterns, and template variables
- **Sync agent** with 3-layer architecture (syncengine → agent → ignition): clone/fetch, staged file sync with orphan cleanup, Ignition scan API integration
- **Mutating webhook** for automatic sidecar injection into annotated pods (native sidecar pattern, K8s 1.28+)
- **Gateway discovery** via pod annotations with status aggregation from agent ConfigMaps
- **Webhook receiver** (`POST /webhook/{namespace}/{crName}`) with auto-detection of GitHub release, ArgoCD, Kargo, and generic payloads; HMAC signature validation
- **Designer session detection** with configurable policy (`proceed`, `wait`, `fail`)
- **Dry-run mode** per profile for diffing without writing to gateway
- **Pause support** at defaults and per-profile levels
- **Helm chart** with cert-manager TLS, agent RBAC, configurable agent image, and helm-docs generated README
- **CI/CD**: lint, test, and release workflows; multi-arch Docker image builds (amd64/arm64)
- **Functional test suite** with phased kind cluster tests (phases 02-09)
- Unit tests with envtest for controller and syncengine

[v0.5.1]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.5.1
[v0.5.0]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.5.0
[v0.4.10]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.10
[v0.4.9]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.9
[v0.4.8]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.8
[v0.4.7]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.7
[v0.4.6]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.6
[v0.4.5]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.5
[v0.4.4]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.4
[v0.4.3]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.3
[v0.4.2]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.2
[v0.4.1]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.1
[v0.4.0]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.4.0
[v0.3.0]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.3.0
[v0.2.0]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.2.0
[v0.1.2]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.1.2
[v0.1.1]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.1.1
[v0.1.0]: https://github.com/ia-eknorr/stoker-operator/releases/tag/v0.1.0
