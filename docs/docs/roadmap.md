---
sidebar_position: 10
title: Roadmap
description: Planned features and milestones for Stoker.
---

# Roadmap

Current version: **v0.6.1**. [See the changelog](https://github.com/ia-eknorr/stoker-operator/blob/main/CHANGELOG.md) for release history.

Items are ordered by priority: the first section is what gets built next. Later sections move up based on user feedback and evidence of need.

## Next: validation and visibility

Make invalid configuration impossible to apply, and make sync behavior observable without reading agent logs.

- CEL validation rules on the GatewaySync CRD: reject invalid CRs at apply time with no webhook round trip; an admission webhook follows only for checks CEL cannot express
- Conflict detection when multiple profiles map to the same destination path
- New condition types: `AgentReady`, `RefSkew`
- Structured audit logging: per-sync JSON record with timestamp, commit, author, gateway, files, and result
- Sync diff report included in the audit record
- Post-sync health verification: confirm project state and tag providers, not just a scan 200
- Drift detection: re-syncing the same commit reports unexpected changes
- `emptyDir` size limit on the agent repo volume (prevents node disk pressure from large repos)
- Webhook receiver rate limiting

## Later: scale

Remove scaling walls once fleet sizes demand it. A scale test (10+ gateways on a single CR) gates this work and measures whether the contention is real before anything gets built.

- Informer-based metadata ConfigMap watch replacing the agent's 3s polling
- Per-gateway status ConfigMap sharding to eliminate write contention at 10+ gateways

## Later: API graduation

Stabilize the API before wider adoption makes breaking changes expensive.

- Graduate `stoker.io/v1alpha1` to `v1beta1` with a conversion path and a field deprecation policy

## Future ideas

These are valuable but not yet scoped into milestones. They'll be prioritized based on user feedback.

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
