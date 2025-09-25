# Adhar Control Plane Module

This module houses the Crossplane v2 based control plane that backs every `adhar` CLI capability.  It consolidates Crossplane package definitions, compositions, and supporting Go utilities that expose a declarative API for the platform.  Each CLI command maps to one or more composite resources managed from this directory.

## What lives here
- **Configuration package** under `configuration/` that can be built into an `.xpkg` and installed onto a Crossplane v2 runtime
- **Feature registry** under `features/` that tracks coverage of CLI commands and their composite resource definitions
- **Supporting Go helpers** under `pkg/` for validating specs and rendering Crossplane manifests
- **Design documentation** under `docs/` describing the architecture, roadmap, and command mapping

## Development workflow
1. Design or update the desired composite API in `configuration/xrd/`
2. Provide one or more provider-specific compositions in `configuration/compositions/`
3. Register the command → resource mapping in `features/registry.yaml`
4. Add helper logic in `pkg/` if the CLI or automation layer requires validation or templating
5. Build and publish the Crossplane package with `make build`

## Building the Crossplane package
```sh
cd control-plane
make build            # produces dist/adhar-control-plane.xpkg
make lint             # static validation of registry + manifests
make clean            # remove build artefacts
```

## Status
The control-plane package now ships multi-provider cluster support (EKS, GKE, AKS, DOKS, Civo) rendered through the Crossplane templating function so any number of node pools can be declared.  The first application-facing APIs for `apps` and `gitops` are defined and wired into the registry, establishing the pattern for ArgoCD-backed deliveries.  Provider-specific knobs (Azure identities, DigitalOcean VPCs/maintenance windows, Civo connection details) are parameterised, and templated ArgoCD resources report sync and health status back to the composite for CLI visibility.

### Current limitations
- Provider compositions focus on the primary fields needed for day-1 provisioning; each provider still needs deeper surface coverage (e.g., advanced networking, identity, and logging options).
- Azure, DigitalOcean, and Civo manifests require the respective Crossplane providers to be pre-configured with credentials; additional guardrails for credential discovery and secret management remain TODOs.
- Application and GitOps compositions target ArgoCD installations reachable via a `kubernetes` provider config; reconcilers surface sync/health signals but do not yet translate individual ArgoCD condition reasons into structured composite conditions.
