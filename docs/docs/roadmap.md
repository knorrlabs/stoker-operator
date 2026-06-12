---
sidebar_position: 10
title: Roadmap
description: Planned features and milestones for Stoker.
---

# Roadmap

Current version: **v0.6.1**. [See the changelog](https://github.com/ia-eknorr/stoker-operator/blob/main/CHANGELOG.md) for release history.

## v0.6.0 — Scale & Operability

Remove scaling walls and make the agent more reactive.

- Informer-based ConfigMap watch replacing 3s polling in agent
- Downward API annotation reader — enables `stoker.io/ref-override` and profile switching without pod restart
- Per-gateway status ConfigMap sharding (eliminate write contention at 10+ gateways)
- `emptyDir` size limit on agent repo volume (prevent node disk pressure from large repos)
- Webhook receiver rate limiting

## v0.7.0 — Conditions & Validation

Operational visibility and safety for fleet management.

- New condition types: `AgentReady`, `RefSkew`
- Drift detection (re-sync same commit reports unexpected changes)
- Post-sync health verification (project state, tag providers — not just scan 200)
- Sync diff report in changes ConfigMap
- Conflict detection when multiple profiles map to the same destination path
- Validating admission webhook for GatewaySync CRs (reject invalid CRs at apply time)
- Structured audit logging (per-sync JSON record: timestamp, commit, author, gateway, files, result)

## Future Ideas

These are valuable but not yet scoped into versioned milestones. They'll be prioritized based on user feedback.

**Safety & Trust:**
- Designer session project-level granularity (sync Project B while designer has Project A open)
- Pre-sync backup with auto-rollback on scan failure
- Module management (`.modl` sync to `modules/` with `postAction: restart`)
- Per-CR webhook HMAC secrets (replace global HMAC)
- Git commit signature verification (GPG/SSH, IEC 62443 compliance)

**Reach:**
- Standalone agent mode (systemd/Windows service for bare-metal Ignition servers)
- Approval annotation gate for production gateways

**Enterprise:**
- Maintenance windows and change freeze schedules
- External audit sink (SIEM integration via webhook/syslog)
- Drift detection with configurable action (report / restore / alert)
- Resource quotas and rate limiting for concurrent syncs
