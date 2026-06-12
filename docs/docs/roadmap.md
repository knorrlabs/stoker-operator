---
sidebar_position: 10
title: Roadmap
description: Planned features and milestones for Stoker.
---

# Roadmap

Current version: **v0.6.1**. [See the changelog](https://github.com/ia-eknorr/stoker-operator/blob/main/CHANGELOG.md) for release history.

Milestones are release targets in priority order, not promises: scope can shift between minors, and the changelog records what actually shipped.

## v0.7.0 - Validation and conditions

Reject invalid configuration at apply time, and surface agent state where `kubectl get gs` can see it.

- CEL validation rules on the GatewaySync CRD: reject invalid CRs at apply time with no webhook round trip
- Conflict detection when multiple profiles map to the same destination path (admission webhook only if CEL cannot express the check)
- New condition types: `AgentReady`, `RefSkew`
- `emptyDir` size limit on the agent repo volume (prevents node disk pressure from large repos)
- Webhook receiver rate limiting

## v0.8.0 - Sync transparency

Answer "what did the last sync actually do" without reading agent logs.

- Structured audit logging: per-sync JSON record with timestamp, commit, author, gateway, files, and result
- Sync diff report included in the audit record
- Post-sync health verification: confirm project state and tag providers, not just a scan 200
- Drift detection: re-syncing the same commit reports unexpected changes

## v0.9.0 - Scale

Remove scaling walls for larger fleets. The scale test lands first and measures the contention before any fix is built.

- Scale e2e test: 10+ gateways on a single CR, asserting sync latency and ConfigMap write behavior
- Informer-based metadata ConfigMap watch replacing the agent's 3s polling
- Per-gateway status ConfigMap sharding to eliminate write contention

## v1.0.0 - Stable API

v1.0.0 graduates the API from `stoker.io/v1alpha1` to `stoker.io/v1beta1`, with conversion between served versions and a deprecation window before `v1alpha1` is removed.

It ships when these criteria hold:

- v0.7.0 through v0.9.0 landed without breaking CRD schema changes, demonstrating the API shape is right
- Invalid configuration is rejected at apply time, not discovered at sync time
- Behavior at 10+ gateways per CR is measured by the scale test and documented
- The upgrade path between minor versions is covered by e2e tests

## Future ideas

These are valuable but not yet scoped into a release. They'll be prioritized into post-1.0 milestones based on user feedback.

**Safety and trust:**

- Designer session project-level granularity (sync Project B while a designer has Project A open)
- Pre-sync backup with automatic rollback on scan failure
- Module management (`.modl` sync to `modules/` with `postAction: restart`)
- Per-CR webhook HMAC secrets replacing the global secret
- Git commit signature verification (GPG/SSH) for IEC 62443 compliance

**Reach:**

- Standalone agent mode (systemd or Windows service for bare-metal Ignition servers)
- Approval annotation gate for production gateways

**Enterprise:**

- Configurable drift action (report, restore, or alert) building on drift detection
- Maintenance windows and change freeze schedules
- External audit sink (SIEM integration via webhook or syslog)
- Resource quotas and rate limiting for concurrent syncs
