# Control Plane Architecture Overview

The Adhar control plane is implemented as a Crossplane v2 configuration package.  It provides composite resources that translate high level platform intents into provider specific infrastructure.  The package is designed around three layers:

1. **Command API layer** – one composite resource per CLI command (or command family) that mirrors the command flags and arguments.
2. **Composition layer** – provider specific compositions that expand command intents into concrete managed resources.
3. **Runtime helpers** – optional Crossplane Functions and external controllers that supply logic which cannot be expressed declaratively.

The goal is to make every action that the `adhar` CLI can perform reproducible by applying a Kubernetes resource.  Platform operators can therefore automate or audit all changes using GitOps tooling.

## Component layout

```
control-plane/
├── configuration/               # Crossplane configuration package
│   ├── crossplane.yaml           # Package metadata
│   ├── xrd/                      # Composite resource definitions (XRDs)
│   ├── compositions/             # Provider specific compositions
│   ├── providers/                # Provider package dependencies and configuration
│   └── functions/                # Crossplane Functions used by compositions (optional)
├── docs/                         # Design and architecture documentation
├── features/                     # Command → resource registry and implementation status
└── pkg/                          # Supporting Go utilities (linting, helpers)
```

## Crossplane v2 considerations

- The package pins `spec.crossplane.version` to Crossplane v2 and follows the v2 package format.
- Compositions target `apiextensions.crossplane.io/v1` XRDs and leverage the v2 Function pipeline for dynamic resource rendering.
- Provider dependencies are expressed using the v2 `spec.dependsOn` semantics.
- Where possible, the schema reflects existing Adhar API types to minimise duplication between the CLI and the declarative layer.

## Implemented features

- **Cluster command (multi-provider)** – `CompositeCluster` (`Cluster` claim) now renders EKS, GKE, AKS, DigitalOcean, and Civo clusters with an arbitrary number of node pools via the shared templating function.
- **Apps command (ArgoCD Application)** – `CompositeApplication` (`Application` claim) describes application deployments that render ArgoCD `Application` manifests through the Kubernetes provider and feeds sync/health status back into the composite.
- **GitOps command (ArgoCD Project & ApplicationSet)** – `CompositeGitOps` (`GitOps` claim) provisions AppProjects and optional ApplicationSets, establishing guardrails for team-specific GitOps workflows while reporting reconciliation signals in composite status.
