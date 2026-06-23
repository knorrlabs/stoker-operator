---
sidebar_position: 3
title: Upgrading
description: Move Stoker between versions safely, including the CRD-on-helm-upgrade footgun, why upgrading the controller does not upgrade the running agents, and the Kubernetes compatibility matrix.
---

# Upgrading

Most Stoker upgrades are a single `helm upgrade`. The three things that are not obvious, and that this guide makes explicit, are:

- **`helm upgrade` never updates the `GatewaySync` CRD.** Helm only installs CRDs from a chart's `crds/` directory on first install. A CRD schema change between versions is silently skipped unless you apply it by hand.
- **Upgrading the controller does not upgrade the running agents.** The `stoker-agent` sidecar is injected at gateway-pod creation, so an already-running gateway keeps its old agent image until its pod is recreated — even after the controller is on the new version and `GatewaySync` reports `Ready`.
- **Each release builds against a specific Kubernetes client version**, which sets the API-server versions it officially supports.

Start with the [routine in-minor upgrade](#routine-in-minor-upgrade-helm). Cross a minor (`0.6.x` to `0.7.x`) only after the [pre-flight](#pre-flight-crossing-a-minor). If you run Stoker under ArgoCD, read the [GitOps appendix](#gitops--argocd-appendix) as well.

## Versioning & upgrade policy

Stoker is pre-1.0, so it follows pre-release SemVer:

- A **minor** bump (`0.6` to `0.7`) may carry breaking changes.
- A **patch** within a minor (`0.6.0` to `0.6.1`) never does. Patches are safe to take blind.

For a `0.x` package, the common version ranges all resolve to the same window. `^0.6.0`, `~0.6.0`, and `0.6.x` each mean `>=0.6.0 <0.7.0`. Pin to a minor and take patches freely; treat a minor bump as a deliberate, reviewed step.

Breaking changes are flagged in the [CHANGELOG](https://github.com/knorrlabs/stoker-operator/blob/main/CHANGELOG.md) three ways:

- A conventional-commit `!` (for example `feat!:`).
- A `### Removed` section.
- A `### Breaking` section.

Before any upgrade, read every CHANGELOG entry between your current version and the target. The CHANGELOG records *what* changed; this guide gives you the *procedure*.

## Kubernetes version compatibility

Each Stoker release pins a set of Kubernetes client libraries (`client-go`, `controller-runtime`). `client-go` supports an API server within plus-or-minus one minor of its own version, which defines the officially supported skew.

| Stoker version | Kubernetes libraries | Officially supported API server |
| --- | --- | --- |
| `0.5.1` and earlier | pre-1.36 | matches that release's libraries |
| `0.5.2` to `0.6.x` | k8s 1.36 (`controller-runtime 0.24`) | **k8s 1.35 to 1.37** |

**Guidance for `0.6.x`: run the operator on Kubernetes 1.35 or newer.**

:::note Older clusters
The APIs Stoker actually calls are long-stable: CRD `v1`, core pods, `rbac/v1`, and `coordination/v1` leases. A cluster older than 1.35 may work in practice, but it is outside the officially supported skew, so treat it as unsupported. Separately, native sidecar injection requires Kubernetes **1.28+** regardless of version (it relies on `initContainer` with `restartPolicy: Always`).
:::

## Routine in-minor upgrade (Helm)

Use this for any patch bump inside a minor (for example `0.6.0` to `0.6.1`).

1. **Review the CHANGELOG** entries between your current version and the target. For a patch bump you are confirming there are no surprises, not deciding whether to proceed.

2. **Upgrade the release:**

   ```bash
   helm upgrade stoker oci://ghcr.io/knorrlabs/charts/stoker-operator \
     -n stoker-system --version <target-version>
   ```

3. **Apply CRD changes by hand if the CRD changed.** `helm upgrade` does not touch `crds/`, so a `GatewaySync` schema change is skipped silently. Check the CHANGELOG for a CRD change between your versions. If there is one, pull the chart and apply the CRD directly:

   ```bash
   helm pull oci://ghcr.io/knorrlabs/charts/stoker-operator \
     --version <target-version> --untar
   kubectl apply -f stoker-operator/crds/
   ```

   `0.6.0` to `0.6.1` has **no** CRD change, so this step is a no-op for that bump. When a release does change the CRD, its CHANGELOG entry says so.

4. **Verify the rollout.** Confirm the controller is healthy and every `GatewaySync` returns to `Ready` / synced:

   ```bash
   kubectl get pods -n stoker-system
   kubectl get gs -A
   ```

   See [Troubleshooting](./reference/troubleshooting.md) if a `GatewaySync` does not return to `Ready`.

   A `Ready` `GatewaySync` here confirms the **controller** upgrade only. The agent sidecars in already-running gateways are still on the old version — see [Updating the agent sidecars](#updating-the-agent-sidecars).

### Worked example: `0.5.1` to `0.6.1`

A two-minor jump that is still a clean upgrade. `0.6.0` removed the `stoker.io/agent-image` pod annotation (see the [pre-flight](#pre-flight-crossing-a-minor) below) and `0.5.2` raised the Kubernetes baseline to 1.36 libraries, so confirm the cluster is on k8s 1.35+ first. Neither `0.5.2`, `0.6.0`, nor `0.6.1` changed the `GatewaySync` CRD, so step 3 stays a no-op. After `helm upgrade`, each `GatewaySync` re-resolves its ref and the agents resync.

## Updating the agent sidecars

`helm upgrade` (or an ArgoCD sync) rolls the **controller** to the new version. It does **not** touch the `stoker-agent` sidecar in gateway pods that are already running.

The webhook injects the sidecar at pod **creation**, so a running gateway keeps whatever agent image it was admitted with. After the controller upgrade, `GatewaySync` returns to `Ready` and the agents keep syncing config — but they are still the **old agent version**. This is easy to miss, and it matters most when a release changes the agent image: for example `v0.7.0` re-homes the images to `ghcr.io/knorrlabs/...`, but existing gateways keep pulling `ghcr.io/ia-eknorr/stoker-agent` until their pods are recreated.

To roll the agents onto the new version, recreate the gateway pods once the controller is healthy:

```bash
kubectl rollout restart statefulset/<gateway> -n <namespace>
```

To keep a replica serving during the cycle, delete the pods one at a time instead, waiting for each to become `Ready` before the next. Confirm the new image landed:

```bash
kubectl get pod <gateway-pod> -n <namespace> \
  -o jsonpath='{range .spec.initContainers[?(@.name=="stoker-agent")]}{.image}{end}'
```

Watch that every `GatewaySync` stays `Ready` / synced through the cycle. Until the pods are recreated you are running a new controller against old agents; check the [CHANGELOG](https://github.com/knorrlabs/stoker-operator/blob/main/CHANGELOG.md) for any controller/agent protocol change across the bump, and don't leave the data plane on the old agent longer than necessary.

:::note ArgoCD
`kubectl rollout restart` writes a `restartedAt` annotation into the StatefulSet's pod template, which ArgoCD sees as drift and may revert under `selfHeal`. To recreate gateway pods without changing tracked spec, **delete the pods** (one at a time) instead — the StatefulSet recreates them with the new sidecar injected and ArgoCD stays in sync.
:::

## Pre-flight: crossing a minor

A minor bump (`0.6.x` to `0.7.x`+) may break things. Run this checklist before you upgrade:

- [ ] **Read the CHANGELOG** for every `!`, `### Removed`, and `### Breaking` entry between your version and the target.
- [ ] **Confirm no removed config keys or annotations are in use.** For example, `0.6.0` removed the `stoker.io/agent-image` pod annotation; pods that still set it are admitted without error, but the annotation is ignored. Move the image into the CR's `spec.agent.image` or set `DEFAULT_AGENT_IMAGE` on the controller.
- [ ] **Apply any CRD schema change** (see step 3 above). `helm upgrade` will not do it for you.
- [ ] **Confirm the cluster Kubernetes version** is within the new build's [supported skew](#kubernetes-version-compatibility).

Only after every box is checked, run the `helm upgrade` and verify as in the routine flow.

## GitOps / ArgoCD appendix

This section is deployment-specific. It applies when Stoker's chart is vendored as an **OCI Helm dependency** in a GitOps repo and reconciled by ArgoCD, rather than installed with `helm` directly.

### Bumping a vendored chart

Bumping the dependency version in `Chart.yaml` is not enough on its own. You also have to refresh the lock and the packaged archive:

```bash
helm dependency update    # regenerates Chart.lock and pulls the new .tgz
```

Commit both the updated `Chart.lock` and the new `charts/*.tgz`. If ArgoCD serves a stale render after the commit, bust its manifest cache with **App → Hard Refresh** in the UI.

The CRD caveat still applies. ArgoCD reconciling the chart does not apply CRD schema changes from `crds/` any more than `helm upgrade` does. Apply CRD changes the same way: `kubectl apply -f` the new CRD, or manage the CRD as a separate tracked resource.

### Rolling back

With ArgoCD auto-sync and `selfHeal: true`, the **UI rollback button does not work.** It reconciles the app straight back to git `HEAD`, undoing the rollback within the sync interval. Roll back by **reverting the bump commit in git**; ArgoCD then syncs to the reverted state. Neither path needs the `argocd` CLI.

:::caution
A rollback restores the controller and agent images, but it does not revert a CRD you applied by hand. If the new version migrated the `GatewaySync` schema, plan the CRD rollback explicitly before you revert the bump commit.
:::
