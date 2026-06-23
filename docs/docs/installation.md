---
sidebar_position: 2
title: Installation
description: Install the Stoker operator on your Kubernetes cluster.
---

# Installation

## Prerequisites

- Kubernetes >= 1.28
- [cert-manager](https://cert-manager.io/) (for webhook TLS)
- Helm 3

## Install cert-manager

Stoker's mutating webhook requires TLS certificates managed by cert-manager:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.yaml
kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=120s
```

## Install the operator

```bash
helm install stoker oci://ghcr.io/ia-eknorr/charts/stoker-operator \
  -n stoker-system --create-namespace
```

Verify:

```bash
kubectl get pods -n stoker-system
```

You should see a `controller-manager` pod in `Running` state.

## Enable sidecar injection

Sidecar injection is enabled by default in all namespaces (except `kube-system` and `kube-node-lease`). Any pod with annotation `stoker.io/inject: "true"` will receive the agent sidecar; no namespace label is needed.

For regulated environments that require explicit namespace opt-in (e.g., IEC 62443 zone boundaries), enable the namespace label requirement:

```bash
helm upgrade stoker oci://ghcr.io/ia-eknorr/charts/stoker-operator \
  -n stoker-system --set webhook.namespaceSelector.requireLabel=true
```

Then label each namespace where injection should be allowed:

```bash
kubectl label namespace <your-namespace> stoker.io/injection=enabled
```

## Agent RBAC

The agent sidecar needs permission to read GatewaySync CRs and write status ConfigMaps. By default, the controller automatically creates a `RoleBinding` in each namespace where a GatewaySync CR exists, binding the discovered gateway ServiceAccounts to the `stoker-agent` ClusterRole. No manual RBAC setup is needed.

To manage agent RBAC externally (e.g., in GitOps-managed environments), disable auto-binding:

```bash
helm upgrade stoker oci://ghcr.io/ia-eknorr/charts/stoker-operator \
  -n stoker-system --set rbac.autoBindAgent.enabled=false
```

Then create RoleBindings manually in each namespace:

```bash
kubectl create rolebinding stoker-agent -n <your-namespace> \
  --clusterrole=stoker-agent \
  --serviceaccount=<your-namespace>:<service-account>
```

:::tip
The default service account name for the [Ignition Helm chart](https://charts.ia.io) is `ignition`.
:::

## Upgrading

```bash
helm upgrade stoker oci://ghcr.io/ia-eknorr/charts/stoker-operator \
  -n stoker-system
```

:::caution CRDs are not updated by `helm upgrade`
Helm installs CRDs from a chart's `crds/` directory only on first install. `helm upgrade` never updates them, so a `GatewaySync` schema change between versions is skipped silently unless you apply it by hand.
:::

See the [Upgrading guide](./guides/upgrading.md) for the full procedure: the version policy, the Kubernetes compatibility matrix, applying CRD changes, and GitOps / ArgoCD specifics.

## Uninstalling

```bash
helm uninstall stoker -n stoker-system
kubectl delete namespace stoker-system
```

:::caution
Uninstalling the operator removes the mutating webhook. Existing agent sidecars will continue running but won't receive new metadata ConfigMap updates.
:::

## Configuration

See [Helm Values](./reference/helm-values.md) for all configurable chart values.
