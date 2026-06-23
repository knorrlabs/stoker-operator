---
sidebar_position: 1
title: Quickstart
description: Get a single Ignition gateway syncing projects from Git in 4 steps.
---

# Quickstart

Get a single Ignition gateway syncing projects from Git in 4 steps.

## Prerequisites

- Kubernetes cluster (v1.28+)
- `kubectl` and `helm` CLI tools
- [cert-manager](https://cert-manager.io) installed (Stoker uses it for webhook TLS)

:::tip Need a cluster?
Install [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation), then:

```bash
kind create cluster --name stoker-quickstart
kubectl cluster-info
```
:::

If cert-manager isn't installed yet, follow the [default static install](https://cert-manager.io/docs/installation/#default-static-install) or any method from the [cert-manager docs](https://cert-manager.io/docs/installation/).

## 1. Install the Stoker operator

```bash
helm install stoker oci://ghcr.io/knorrlabs/charts/stoker-operator \
  -n stoker-system --create-namespace
```

Verify the controller is running:

```bash
kubectl get pods -n stoker-system
```

You should see a `controller-manager` pod in `Running` state.

## 2. Create secrets

This quickstart uses [`ia-eknorr/test-ignition-project`](https://github.com/ia-eknorr/test-ignition-project), a public example repository created for this guide. It contains an Ignition stack with two gateway configurations (`ignition-blue` and `ignition-red`), each with their own projects and config directories. We'll sync `ignition-blue` to a single gateway.

Create a namespace and a secret so the agent can authenticate with the gateway's scan API. The example repository includes a pre-configured API token resource:

```bash
kubectl create namespace quickstart
kubectl create secret generic gw-api-key -n quickstart \
  --from-literal=apiKey="ignition-api-key:CYCSdRgW6MHYkeIXhH-BMqo1oaqfTdFi8tXvHJeCKmY"
```

:::warning
This API key is for the example repository only. Never reuse example credentials. [Generate unique API tokens](https://www.docs.inductiveautomation.com/docs/8.3/platform/security/api-keys#using-api-keys) for each gateway in your own deployments.
:::

No git credentials are needed since we're using a public repository.

## 3. Create a GatewaySync CR

The GatewaySync CR defines the git repository and sync profiles. The default gateway port (8088) and TLS (false) match the default Ignition Helm chart, so we only need to provide the API key secret:

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: quickstart
  namespace: quickstart
spec:
  git:
    repo: "https://github.com/ia-eknorr/test-ignition-project.git"
    ref: "main"
  gateway:
    api:
      secretName: gw-api-key
  sync:
    profiles:
      standard:
        mappings:
          - source: "services/ignition-blue/projects/"
            destination: "projects/"
            type: dir
            required: true
          - source: "services/ignition-blue/config/"
            destination: "config/"
            type: dir
        syncPeriod: 30
EOF
```

Verify the controller resolved the git ref:

```bash
kubectl get gatewaysyncs -n quickstart
```

The `REF` column should show `main` and `STATUS` should indicate the current state. `READY` will be `False` until a gateway is deployed and synced. Use `-o wide` to see `COMMIT`, `PROFILES`, and `LAST SYNC` columns.

## 4. Deploy an Ignition gateway

Install using the [official Ignition Helm chart](https://charts.ia.io) with Stoker annotations.

```bash
helm repo add inductiveautomation https://charts.ia.io
helm repo update
```

Create a values file that enables auto-commissioning and adds the Stoker sidecar injection annotations:

```yaml title="ignition-values.yaml"
commissioning:
  edition: standard
  acceptIgnitionEULA: true

gateway:
  preconfigure:
    additionalCmds:
      - |
        [ -f "/data/commissioning.json" ] || echo "{}" > /data/commissioning.json

podAnnotations:
  stoker.io/inject: "true"
  stoker.io/cr-name: quickstart
  stoker.io/profile: standard
```

```bash
helm upgrade --install ignition inductiveautomation/ignition \
  -n quickstart --create-namespace -f ignition-values.yaml
```

The key annotations:

| Annotation | Value | Purpose |
|---|---|---|
| `stoker.io/inject` | `"true"` | Triggers sidecar injection |
| `stoker.io/cr-name` | `"quickstart"` | Links to the GatewaySync CR |
| `stoker.io/profile` | `"standard"` | Selects the sync profile from `spec.sync.profiles` |

The webhook injects on pod creation, so the operator and CRs must be running before the gateway pod starts.

Wait for the gateway to start:

```bash
kubectl get pods -n quickstart -w
```

You should see the Ignition pod with **2/2** containers ready (the gateway + the `stoker-agent` sidecar).

## Verify the deployment

Once the gateway pod shows **2/2**, walk through these checks to confirm everything is wired up correctly.

### Confirm sidecar injection

Verify the pod has both containers: the gateway and the injected `stoker-agent` sidecar.

```bash
kubectl get pod -n quickstart -o 'custom-columns=NAME:.metadata.name,SIDECARS:.spec.initContainers[*].name,STATUS:.status.phase'
```

You should see `stoker-agent` listed as an init container (native sidecar).

### Check events

Look at the namespace events to see the injection and sync activity:

```bash
kubectl get events -n quickstart --sort-by=.lastTimestamp | tail -15
```

### Check the GatewaySync CR status

```bash
kubectl get gs -n quickstart
```

After 1-2 minutes you should see:

```text
NAME         REF    GATEWAYS     READY   STATUS              AGE
quickstart   main   1/1 synced   True    All gateways synced 5m
```

### Describe the GatewaySync CR

For detailed status including conditions and discovered gateways:

```bash
kubectl describe gatewaysync quickstart -n quickstart
```

Look for:

- **Conditions:** `RefResolved=True`, `AllGatewaysSynced=True`, and `Ready=True`
- **Discovered Gateways:** should list the gateway pod with its sync status and commit hash

### Read the agent logs

```bash
kubectl logs -n quickstart -l app.kubernetes.io/name=ignition -c stoker-agent --tail=20
```

Look for:

- `clone complete`: the repo was cloned successfully
- `initial sync complete, startup probe now passing`: files were delivered before the gateway started
- `new commit detected`: the agent saw a commit change and will sync

In steady state, agent logs are silent when nothing changes. To see detailed sync activity (file counts, scan results), increase the log verbosity with `--zap-log-level=1` or set `LOG_LEVEL=debug`.

### Inspect the status ConfigMap

The agent writes detailed sync status to a ConfigMap:

```bash
kubectl get cm stoker-status-quickstart -n quickstart -o jsonpath='{.data}' | python3 -m json.tool
```

This shows the synced commit, file counts, project names, and any error messages per gateway.

## Explore

Open the Ignition web UI to see the synced projects:

```bash
kubectl port-forward -n quickstart svc/ignition 8088:8088
```

Navigate to `http://localhost:8088` in your browser. After completing the initial commissioning wizard, you should see the project from the example repository.

Try changing the git ref to a specific tag:

```bash
kubectl patch gatewaysync quickstart -n quickstart --type=merge \
  -p '{"spec":{"git":{"ref":"0.2.0"}}}'
```

Watch the agent pick up the change:

```bash
kubectl get gs -n quickstart -w
```

## Cleanup

```bash
helm uninstall ignition -n quickstart
kubectl delete namespace quickstart
helm uninstall stoker -n stoker-system
kubectl delete namespace stoker-system
```

If you created a kind cluster:

```bash
kind delete cluster --name stoker-quickstart
```

## Next steps

- **[Multi-Gateway Profiles](./guides/multi-gateway.md):** use `{{.GatewayName}}` or `{{.Labels.key}}` to serve multiple gateways from one profile
- **[Webhook Sync](./guides/webhook-sync.md):** trigger syncs on git push events instead of polling
- **[Git Authentication](./guides/git-authentication.md):** set up token, SSH, or GitHub App auth for private repositories
- **[GatewaySync CR Reference](./reference/gatewaysync-cr.md):** full spec reference including git auth, polling, sync profiles, and agent configuration
- **[Helm Values](./reference/helm-values.md):** all configurable values for the operator chart
