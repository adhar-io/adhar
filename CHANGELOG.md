# Changelog

All notable changes to the Adhar Platform will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Planned
- Enhanced multi-tenancy support
- Advanced networking policies
- Extended control plane capabilities
- Platform marketplace

---

## [0.3.8] - 2025-11-15

### Added
- **Gitea Repository Status Tracking**: Added `repositoriesCreated` field to prevent redundant API calls
- **Documentation Consolidation**: Restructured documentation from 16 to 6 focused files
  - Created comprehensive `docs/README.md` as documentation hub
  - Added `USER_GUIDE.md` consolidating platform capabilities and configuration
  - Added `ADVANCED.md` for production deployment and HA mode
  - Added `RELEASE_GUIDE.md` for release management
- **Configuration Templates**: Sanitized `config.yaml` with placeholder values for security
- **Build System Improvements**: Updated Dockerfile to use Go 1.23-alpine
  - Fixed build vulnerabilities by using valid Go version
  - Added necessary build dependencies (git, ca-certificates)

### Changed
- **Goreleaser Configuration**: Updated to match new project structure
  - Fixed ldflags to use correct version package variables
  - Added Windows support with proper binary packaging
  - Enhanced changelog generation with GitHub integration
- **Documentation Structure**: Consolidated overlapping content
  - Merged `PLATFORM_GUIDE.md` into `USER_GUIDE.md`
  - Merged `HA_MODE_CONTROL.md` into `ADVANCED.md`
  - Merged `MIGRATION_GUIDE.md` into `ADVANCED.md`
  - Removed outdated files (`IMPLEMENTATION_HISTORY.md`, `crossplane-v2.1-upgrade.md`)
- **Provider References**: Renamed `PROVIDER_SYSTEM_GUIDE.md` to `PROVIDER_GUIDE.md`

### Fixed
- **Gitea Reconciliation Loop**: Controller no longer attempts to recreate existing repositories
  - Prevents 409 Conflict errors in Gitea logs
  - Improved controller efficiency with status tracking
- **Documentation Links**: Fixed all broken internal documentation links
  - Updated main `README.md` with correct documentation references
  - Fixed cross-references between documentation files
  - Corrected `CONTRIBUTING.md` path references
- **Docker Build Issues**: Resolved Dockerfile build failures
  - Changed from non-existent `golang:1.24` to `golang:1.23-alpine`
  - Fixed build command from `cmd/main.go` to `./cmd`
  - Added missing `globals/` directory to COPY commands
- **Go Module**: Updated `go.mod` from `go 1.24.2` to `go 1.23` for compatibility

### Security
- **Configuration Security**: Replaced all tokens and secrets in `config.yaml` with placeholders
  - AWS, Azure, GCP credentials sanitized
  - DigitalOcean and Civo API tokens replaced
  - Email addresses and project IDs replaced with placeholders
- **Build Security**: Alpine-based builder reduces attack surface

### Documentation
- Added comprehensive `RELEASE_GUIDE.md` with release procedures
- Added `CHANGELOG.md` for tracking all changes
- Added `SECURITY.md` with vulnerability reporting process
- Updated all documentation files to v0.3.8
- Fixed 10+ broken documentation links
- Created clear documentation hierarchy and navigation

---

## [0.3.7] - 2025-10-01

### Added
- Enhanced provider validation and error handling
- Improved CLI progress indicators
- Additional platform service templates

### Fixed
- Provider credential handling edge cases
- ArgoCD sync timeout issues
- Nginx ingress configuration for Kind clusters

---

## [0.3.6] - 2025-09-15

### Added
- Civo provider support (6th production provider)
- Advanced HA mode configuration options
- Platform service health checks

### Changed
- Improved error messages and debugging output
- Enhanced platform service deployment order
- Updated Cilium to v1.15.7

### Fixed
- Memory optimization for local Kind clusters
- Service mesh connectivity issues
- Certificate renewal automation

---

## [0.3.5] - 2025-08-28

### Added
- Azure (AKS) provider support
- Cross-provider migration tools
- Enhanced monitoring dashboards

### Changed
- Optimized provider initialization
- Improved cluster provisioning speed
- Updated ArgoCD to v2.11

### Fixed
- Network policy compatibility issues
- Provider-specific resource cleanup
- Gitea repository initialization timing

---

## [0.3.0] - 2025-07-15

### Added
- Multi-cloud provider architecture complete
- Management cluster first approach
- GitOps-driven platform services
- 5 production-ready providers: Kind, AWS, GCP, DigitalOcean, and Azure (in progress)
- Unified CLI experience across all providers
- Template-based manifest generation with KCL

### Changed
- Migrated from direct kubectl to GitOps workflow
- Improved provider abstraction layer
- Enhanced security with Cilium network policies

### Security
- Zero-trust networking enabled by default
- Secrets management with HashiCorp Vault integration
- Policy enforcement with Kyverno

---

## [0.2.0] - 2025-05-01

### Added
- Kind provider for local development
- Core platform services integration
- Basic multi-cloud abstractions
- CLI foundation

### Changed
- Refactored provider interface
- Improved error handling
- Enhanced logging

---

## [0.1.0] - 2025-03-01

### Added
- Initial release
- Basic cluster provisioning
- Core Kubernetes operations
- Proof of concept implementation

---

## Version History Summary

| Version | Release Date | Major Changes |
|---------|--------------|---------------|
| 0.3.8 | 2025-11-15 | Documentation consolidation, security fixes |
| 0.3.7 | 2025-10-01 | Provider validation improvements |
| 0.3.6 | 2025-09-15 | Civo provider, HA enhancements |
| 0.3.5 | 2025-08-28 | Azure provider support |
| 0.3.0 | 2025-07-15 | Multi-cloud architecture complete |
| 0.2.0 | 2025-05-01 | Platform services integration |
| 0.1.0 | 2025-03-01 | Initial release |

---

## Upgrade Notes

### Upgrading to 0.3.8

No breaking changes. Simply update the binary:

```bash
# Download latest release
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/scripts/install.sh | bash

# Verify version
adhar version
```

### Upgrading from 0.2.x to 0.3.x

Major architectural changes. See [Migration Guide](docs/ADVANCED.md#migration-strategies) for details.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for information on how to contribute to this project.

## Security

See [SECURITY.md](SECURITY.md) for information on reporting security vulnerabilities.

---

[Unreleased]: https://github.com/adhar-io/adhar/compare/v0.3.8...HEAD
[0.3.8]: https://github.com/adhar-io/adhar/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/adhar-io/adhar/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/adhar-io/adhar/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/adhar-io/adhar/compare/v0.3.0...v0.3.5
[0.3.0]: https://github.com/adhar-io/adhar/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/adhar-io/adhar/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/adhar-io/adhar/releases/tag/v0.1.0

