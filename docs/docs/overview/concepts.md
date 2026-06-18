---
sidebar_position: 3
title: Concepts
description: Key Kubernetes and Stoker terms used throughout the docs.
---

# Concepts

Stoker is a Kubernetes operator for Ignition SCADA gateways. This page defines key Kubernetes and Stoker-specific terms used throughout the docs.

## Kubernetes concepts

| Term | What it means |
|------|--------------|
| **Cluster** | A set of machines (nodes) running Kubernetes. Think of it as the infrastructure that hosts your gateways. |
| **Namespace** | A logical partition within a cluster. Similar to folders; you might have `production` and `staging` namespaces for different environments. |
| **Pod** | The smallest deployable unit in Kubernetes. A pod runs one or more containers. Your Ignition gateway runs inside a pod. |
| **Sidecar** | An extra container that runs alongside your main container in the same pod. Stoker's sync agent runs as a sidecar next to the Ignition gateway container. |
| **Operator** | A program that watches for custom resources and takes action. Stoker's controller is an operator: it watches `GatewaySync` resources and manages sync behavior. |
| **Custom Resource (CR)** | An extension of the Kubernetes API. `GatewaySync` is a custom resource that you create to tell Stoker what to sync and how. |
| **CRD (Custom Resource Definition)** | The schema that defines a custom resource type. The `GatewaySync` CRD tells Kubernetes what fields are valid. |
| **ConfigMap** | A Kubernetes object that stores key-value configuration data. Stoker uses ConfigMaps to pass metadata from the controller to agents and to collect sync status. |
| **Annotation** | A key-value label attached to a Kubernetes object for metadata. Stoker uses annotations like `stoker.io/inject: "true"` to control which pods get the agent sidecar. |
| **Label** | Similar to annotations but used for selection and filtering. Gateway pods use labels like `app.kubernetes.io/name` for discovery. |
| **Helm** | A package manager for Kubernetes. Stoker is installed via a Helm chart that templates all the Kubernetes resources. |
| **MutatingWebhook** | A Kubernetes feature that intercepts object creation and modifies it. Stoker's webhook automatically injects the agent sidecar into gateway pods; no manual sidecar configuration needed. |
| **RBAC (Role-Based Access Control)** | Kubernetes permission system. The agent sidecar needs permissions to read ConfigMaps in its namespace. |
| **kubectl** | The command-line tool for interacting with a Kubernetes cluster. Used throughout this guide to apply resources, inspect status, and view logs. |

## Stoker-specific terms

These terms are specific to Stoker and appear throughout the documentation.

| Term | What it means |
|------|--------------|
| **GatewaySync** | The custom resource (CR) you create to define a sync. It specifies the Git repo, ref, authentication, gateway settings, and sync profiles. Short name: `gs`. |
| **Profile** | A named set of file mappings within a GatewaySync CR. Different gateways can use different profiles to get different subsets of the repo. Selected by the `stoker.io/profile` pod annotation. |
| **Mapping** | A source-to-destination rule inside a profile. For example, `source: "projects/"` → `destination: "projects/"` copies the `projects/` directory from Git to the gateway's data directory. |
| **Template variable** | Placeholders like `{{.GatewayName}}`, `{{.PodOrdinal}}`, or `{{.Vars.key}}` in mapping paths and patch values. Resolved per-gateway at sync time so one profile can route different files to different gateways. Label and var keys must be valid identifiers (letters, digits, underscores; no dashes). |
| **Ref resolution** | The process of converting a branch name or tag to a specific Git commit SHA via `git ls-remote`. The controller does this without cloning the repo. |
| **Metadata ConfigMap** | `stoker-metadata-{crName}`: written by the controller, read by agents. Contains the resolved ref, commit, auth type, and profile mappings. |
| **Status ConfigMap** | `stoker-status-{crName}`: written by agents, read by the controller. Contains per-gateway sync results, error messages, and file change counts. |
| **Webhook receiver** | An HTTP endpoint (`POST /webhook/{namespace}/{crName}`) that accepts push events from GitHub, ArgoCD, Kargo, or any system that sends JSON. Triggers an immediate sync instead of waiting for the poll interval. |
| **Native sidecar** | A Kubernetes 1.28+ feature where init containers can have `restartPolicy: Always`, making them run alongside the main container for the pod's lifetime. Stoker uses this for the agent. |

