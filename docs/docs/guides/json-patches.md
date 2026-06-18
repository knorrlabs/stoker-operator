---
sidebar_position: 4
title: JSON Patches
description: Apply targeted JSON field updates at sync time without modifying source files.
---

# JSON Patches

JSON patches let the agent update specific fields in JSON files at staging time, without modifying source files in git. Use them when per-gateway values (database hosts, system names, port numbers) need to differ and you want surgical control over exactly which fields change, rather than treating an entire file as a Go template.

## How it works

Add a `patches` block to any mapping. Each patch specifies:
- **`file`:** which file(s) to patch, expressed as a path relative to the mapping's destination (supports doublestar globs). For file mappings, `file` can be omitted and defaults to the mapped file itself.
- **`set`:** a map of [sjson-style dot-notation paths](https://github.com/tidwall/sjson#path-syntax) to values. Values may contain Go template syntax (the same variables available in `template: true`).

The agent copies files from git into staging, then applies patches in-place before writing to `/ignition-data/`. Source files in git are never modified.

## When to use patches vs `template: true`

| Use case | Best tool |
|----------|-----------|
| Override a few specific JSON field values per gateway | **`patches`** |
| Files authored with `{{...}}` syntax intentionally | **`template: true`** |
| Non-JSON text files with variable placeholders | **`template: true`** |
| Updating database hosts, system names, ports in JSON | **`patches`** |
| Files mix binary and text (images alongside JSON) | **`patches`** on JSON only |

`patches` only works on valid JSON files and will error on anything else, including files that fail JSON parsing. `template: true` works on any text file but requires `{{...}}` syntax to already be in the source.

## Patch value type inference

Patch values are resolved as Go templates first, then type-inferred before being set:

| Value string | Inferred type | Result in JSON |
|---|---|---|
| `"true"` / `"false"` | bool | `true` / `false` |
| `"9090"`, `"3.14"` | number | `9090`, `3.14` |
| `"\"quoted string\""` | string | `"quoted string"` |
| `"my-gateway"` | string (fallback) | `"my-gateway"` |
| `"null"` | null | `null` |

This means you can set boolean flags, numeric ports, and string values without quoting: just write the value you want.

## Examples

### Directory mapping: patch a specific file

Sync a config directory and update the `systemName` field in one JSON file:

```yaml
spec:
  sync:
    profiles:
      default:
        mappings:
          - source: "config/resources/ignition/core"
            destination: "config/resources/ignition/core"
            patches:
              - file: "system-properties/config.json"
                set:
                  systemName: "{{ .GatewayName }}"
```

The source directory `config/resources/ignition/core` is copied as-is from git. After staging, the agent opens `config/resources/ignition/core/system-properties/config.json` and sets `systemName` to the gateway's resolved name.

All other files in the directory are unmodified.

### File mapping: patch the mapped file itself

When the mapping targets a single file, omit `file` and the patch applies to that file:

```yaml
mappings:
  - source: "config/versions/.versions.json"
    destination: "config/versions/.versions.json"
    patches:
      - set:
          gatewayVersion: "{{ .Vars.gatewayVersion }}"
```

:::note
For file mappings, `file` defaults to the base name of the mapped file when omitted. If you specify `file`, it must match the base filename exactly (globs are supported but anchored to the file's parent directory).
:::

### Glob: patch multiple files in a directory

Use a doublestar glob to patch all matching files in a directory mapping:

```yaml
mappings:
  - source: "config/db-connections"
    destination: "config/db-connections"
    patches:
      - file: "*.json"
        set:
          host: "{{ .Vars.dbHost }}"
          port: "{{ .Vars.dbPort }}"
```

Every `.json` file in `config/db-connections/` gets its `host` and `port` fields updated. Non-JSON files (or files in subdirectories not matched by the glob) are untouched.

### Multiple patches on one mapping

A single mapping can carry multiple patch blocks, each targeting different files or setting different fields:

```yaml
mappings:
  - source: "config/resources/ignition/core"
    destination: "config/resources/ignition/core"
    patches:
      - file: "system-properties/config.json"
        set:
          systemName: "{{ .GatewayName }}"
          httpPort: "{{ .Vars.gatewayPort }}"
      - file: "redundancy/config.json"
        set:
          masterHostname: "{{ .Vars.masterHost }}"
          backupHostname: "{{ .Vars.backupHost }}"
      - file: "historian-providers/*.json"
        set:
          connection.host: "{{ .Vars.historyDbHost }}"
```

### Patches with `vars`

Combine patches with `spec.sync.defaults.vars` for environment-wide values:

```yaml
spec:
  sync:
    defaults:
      vars:
        dbHost: "postgres.prod.svc.cluster.local"
        dbPort: "5432"
    profiles:
      default:
        mappings:
          - source: "config/db-connections"
            destination: "config/db-connections"
            patches:
              - file: "*.json"
                set:
                  host: "{{ .Vars.dbHost }}"
                  port: "{{ .Vars.dbPort }}"
```

Profile-level `vars` override default `vars` per-key, so you can have a development namespace with different DB hosts while sharing the same profile structure.

## Type inference

:::note
The `type` field in a mapping is optional. When omitted, the agent calls `os.Stat` on the source path at sync time to determine whether it is a file or directory. If you specify `type`, it is validated against the filesystem; a mismatch produces an error. If the source path does not exist and `required: false`, the agent falls back to `"dir"` and skips the mapping silently.
:::

## sjson path syntax

Paths follow [tidwall/sjson](https://github.com/tidwall/sjson#path-syntax) dot-notation:

| Path | Effect |
|------|--------|
| `systemName` | Top-level key |
| `connection.host` | Nested key |
| `networkInterfaces.0.address` | Array element by index |
| `tags.-1` | Append to array |

Paths are case-sensitive. Keys containing `.` or special characters must be escaped. See the sjson docs for details.

## Patches and `template: true` together

`patches` and `template: true` can be set on the same mapping. When both are enabled, `template: true` rendering runs first (resolving `{{...}}` in file contents), then patches are applied. This lets you combine authored template syntax with surgical field overrides on the same files.

```yaml
mappings:
  - source: "config/system-properties"
    destination: "config/resources/local/ignition/system-properties"
    template: true      # resolves {{.GatewayName}} etc. in all text files
    patches:
      - file: "config.json"
        set:
          httpPort: "{{ .Vars.gatewayPort }}"   # also override the port
```

## Error messages

| Error | Cause | Fix |
|-------|-------|-----|
| `file is not valid JSON` | A matched file isn't valid JSON | Patches only work on JSON files; check the glob or `file` field |
| `invalid patch file pattern "<pattern>"` | Malformed doublestar glob | Fix the glob syntax in the `file` field |
| `sjson.Set "<path>": ...` | sjson path error | Check path syntax against the sjson docs |
| `resolving patch value for path "<path>"` | Template error in a `set` value | Check `{{...}}` syntax and that referenced vars exist |
| `type mismatch: spec says "dir" but <path> is a file` | Explicit `type` field doesn't match filesystem | Remove `type` to let it be inferred, or fix the value |

## Full example

```yaml
apiVersion: stoker.io/v1alpha1
kind: GatewaySync
metadata:
  name: site-sync
  namespace: production
spec:
  git:
    repo: "https://github.com/org/ignition-config.git"
    ref: "main"
    auth:
      githubApp:
        appId: 123456
        installationId: 789012
        privateKeySecretRef:
          name: github-app-key
          key: privateKey
  gateway:
    port: 8088
    api:
      secretName: gw-api-key
  sync:
    defaults:
      vars:
        dbHost: "postgres.prod.svc.cluster.local"
        dbPort: "5432"
        gatewayPort: "8088"
      excludePatterns:
        - "**/.git/"
        - "**/.gitkeep"
        - "**/.resources/**"
        - "**/.uuid"
    profiles:
      site:
        mappings:
          # Core config: patch system name and DB connections in-place
          - source: "config/resources/ignition/core"
            destination: "config/resources/ignition/core"
            patches:
              - file: "system-properties/config.json"
                set:
                  systemName: "{{ .GatewayName }}"
                  httpPort: "{{ .Vars.gatewayPort }}"
              - file: "db-connections/*.json"
                set:
                  connection.host: "{{ .Vars.dbHost }}"
                  connection.port: "{{ .Vars.dbPort }}"

          # Projects: sync as-is, no templating needed
          - source: "projects"
            destination: "projects"
            required: true
```
