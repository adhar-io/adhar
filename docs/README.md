# Adhar Platform Documentation

**Version**: v0.1.0

Adhar is an open Internal Developer Platform: one command provisions a complete, production-grade, customizable platform from 50+ open-source components, on six clouds or your laptop. This is the documentation hub — every guide below is current with the v0.1.x architecture.

---

## Start Here

| I want to… | Read |
|------------|------|
| **Try it** — running platform in 10 minutes | [Getting Started](GETTING_STARTED.md) |
| **Use it** — daily workflows, CLI, deploying apps | [User Guide](USER_GUIDE.md) |
| **Understand it** — design, components, and why | [Architecture](ARCHITECTURE.md) |
| **Shape it** — packages, environments, extensions | [Customization Guide](CUSTOMIZATION.md) |
| **Run it for real** — HA, hardening, backup/DR, upgrades | [Production Guide](PRODUCTION.md) |
| **Point it at a cloud** — per-provider setup | [Provider Guide](PROVIDER_GUIDE.md) |
| **See where it's going** | [Roadmap](ROADMAP.md) |

## The Documentation Set

### Guides

- **[Getting Started](GETTING_STARTED.md)** — install, `adhar up`, tour the services, first GitOps change, teardown
- **[User Guide](USER_GUIDE.md)** — the platform mental model, CLI reference, deploying applications, requesting infrastructure, observability, day-to-day troubleshooting
- **[Customization Guide](CUSTOMIZATION.md)** — all nine extension points with walkthroughs: package toggles, values, custom packages, `CustomPackage` apps, environments, config layers, Crossplane APIs, providers, foundation tuning
- **[Production Guide](PRODUCTION.md)** — topology selection, HA sizing, security hardening checklist, edge (DNS/TLS/LB), backup & disaster recovery with runbooks, upgrades, day-2 operations
- **[Provider Guide](PROVIDER_GUIDE.md)** — the provider abstraction and setup for Kind, AWS, Azure, GCP, DigitalOcean, Civo, and bring-your-own-cluster

### Architecture & Design

- **[Architecture](ARCHITECTURE.md)** — goals, design principles, the four platform layers, bootstrap/GitOps lifecycle, networking, security, topologies (local → single-cluster → management + workload clusters), extensibility model, quality attributes
- **[Control Plane In Depth](CONTROL_PLANE.md)** — the Crossplane v2 control plane from first principles: how it's built (23 XRDs, 34 Compositions), integrated, and operated — no prior Crossplane knowledge assumed
- **[Architecture Decision Records](adr/README.md)** — the ten load-bearing decisions and how to propose new ones
- **[Roadmap](ROADMAP.md)** — the phased path: local excellence → single-cluster production → multi-cluster platform → developer-experience ecosystem

### Project

- **[Contributing](../CONTRIBUTING.md)** — how to contribute (DCO required)
- **[Release Guide](RELEASE_GUIDE.md)** — versioning and the automated GoReleaser/GitHub Actions pipeline
- **[Security Policy](../SECURITY.md)** — reporting vulnerabilities, supported versions
- **[Changelog](../CHANGELOG.md)** — version history

## The Platform in One Paragraph

`adhar up` bootstraps a strictly ordered foundation — Cilium (CNI + Gateway API) → ArgoCD → Gitea — then seeds the in-cluster Git repos and hands control to GitOps: a single ApplicationSet deploys every enabled package from Git, a Crossplane v2 control plane provides namespaced self-service infrastructure APIs, and from that moment on every platform change is a reviewable Git commit. The same architecture runs on a laptop (Kind), a single production cluster, or a management cluster governing fleets of workload clusters. Details: [Architecture](ARCHITECTURE.md).

## Getting Help

| Channel | Use for |
|---------|---------|
| [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww) | Questions, real-time help |
| [GitHub Issues](https://github.com/adhar-io/adhar/issues) | Bugs, feature requests |
| [GitHub Discussions](https://github.com/adhar-io/adhar/discussions) | Ideas, design conversations |
| [examples/](../examples/) | Sample resources for every CRD |
