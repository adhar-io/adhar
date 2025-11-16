# Adhar Platform Release Guide

**Version**: v0.3.8  
**Last Updated**: November 2025

---

## 📋 Table of Contents

1. [Release Process Overview](#release-process-overview)
2. [Release Types](#release-types)
3. [Pre-Release Checklist](#pre-release-checklist)
4. [Release Steps](#release-steps)
5. [Post-Release Tasks](#post-release-tasks)
6. [Hotfix Process](#hotfix-process)
7. [Rollback Procedures](#rollback-procedures)
8. [Release Schedule](#release-schedule)

---

## Release Process Overview

Adhar follows semantic versioning (SemVer) and uses a structured release process to ensure quality and stability.

### Version Format

```
MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]

Examples:
- v0.3.8 (stable release)
- v0.4.0-beta.1 (pre-release)
- v1.0.0-rc.2 (release candidate)
```

### Version Components

- **MAJOR**: Breaking changes, incompatible API changes
- **MINOR**: New features, backwards-compatible
- **PATCH**: Bug fixes, backwards-compatible
- **PRERELEASE**: Alpha, beta, rc (release candidate)
- **BUILD**: Build metadata (commit hash, build number)

---

## Release Types

### Major Release (x.0.0)

**When to Release:**
- Breaking API changes
- Major architectural changes
- Removed deprecated features
- Incompatible configuration changes

**Timeline:** Every 6-12 months

**Example:** v1.0.0, v2.0.0

### Minor Release (0.x.0)

**When to Release:**
- New features
- New provider support
- New platform capabilities
- Backwards-compatible enhancements

**Timeline:** Every 1-2 months

**Example:** v0.4.0, v0.5.0

### Patch Release (0.0.x)

**When to Release:**
- Bug fixes
- Security patches
- Documentation updates
- Performance improvements

**Timeline:** As needed (typically 1-2 weeks)

**Example:** v0.3.9, v0.3.10

### Pre-Release

**Types:**
- **Alpha** (`v0.4.0-alpha.1`): Early testing, unstable
- **Beta** (`v0.4.0-beta.1`): Feature complete, testing phase
- **RC** (`v0.4.0-rc.1`): Release candidate, final testing

---

## Pre-Release Checklist

### Code Quality

- [ ] All tests passing (unit, integration, e2e)
- [ ] Linter checks passing (`make lint`)
- [ ] Code coverage meets threshold (>70%)
- [ ] No critical security vulnerabilities
- [ ] All provider tests validated

### Documentation

- [ ] CHANGELOG.md updated with all changes
- [ ] Documentation updated for new features
- [ ] API changes documented
- [ ] Migration guide updated (if needed)
- [ ] README.md version updated

### Dependencies

- [ ] Go dependencies updated and verified
- [ ] Kubernetes version compatibility tested
- [ ] Provider SDK versions verified
- [ ] Security vulnerabilities scanned

### Testing

- [ ] Manual testing on all 6 providers
- [ ] Upgrade path tested from previous version
- [ ] Rollback procedures verified
- [ ] Performance benchmarks run
- [ ] Load testing completed (for major releases)

### Platform Services

- [ ] All core services deploy successfully
- [ ] ArgoCD sync working
- [ ] Gitea repositories accessible
- [ ] Monitoring stack operational
- [ ] Security policies enforced

---

## Release Steps

### 1. Prepare Release Branch

```bash
# Create release branch from main
git checkout main
git pull origin main
git checkout -b release/v0.4.0

# Update version in relevant files
./scripts/update-version.sh v0.4.0
```

### 2. Update Documentation

```bash
# Update CHANGELOG.md
cat >> CHANGELOG.md << 'EOF'
## [0.4.0] - 2025-11-20

### Added
- New feature X
- Provider Y support

### Changed
- Improved Z performance

### Fixed
- Bug in component A

### Security
- Updated dependency B
EOF

# Update version in files
sed -i '' 's/v0.3.8/v0.4.0/g' README.md
sed -i '' 's/v0.3.8/v0.4.0/g' docs/**/*.md
```

### 3. Run Pre-Release Tests

```bash
# Run full test suite
make test

# Run linter
make lint

# Run security scan
make security-scan

# Build all platforms
make build-all

# Test on all providers
./scripts/test-all-providers.sh
```

### 4. Create Release Commit

```bash
# Commit version updates
git add .
git commit -m "chore: prepare release v0.4.0"
git push origin release/v0.4.0

# Create pull request to main
gh pr create --title "Release v0.4.0" \
  --body "$(cat CHANGELOG.md | sed -n '/## \[0.4.0\]/,/## \[/p')"
```

### 5. Create Git Tag

```bash
# After PR is merged
git checkout main
git pull origin main

# Create annotated tag
git tag -a v0.4.0 -m "Release v0.4.0

$(cat CHANGELOG.md | sed -n '/## \[0.4.0\]/,/## \[/p')
"

# Push tag
git push origin v0.4.0
```

### 6. Build Release Artifacts

```bash
# Build binaries for all platforms (automated by CI)
goreleaser release --clean

# Build Docker images
docker buildx build --platform linux/amd64,linux/arm64 \
  -t adhar/adhar:v0.4.0 \
  -t adhar/adhar:latest \
  --push .

# Build Helm charts (if applicable)
helm package charts/adhar --version 0.4.0
```

### 7. Publish Release

```bash
# Create GitHub release (automated by CI)
gh release create v0.4.0 \
  --title "Adhar v0.4.0" \
  --notes "$(cat CHANGELOG.md | sed -n '/## \[0.4.0\]/,/## \[/p')" \
  dist/*

# Publish to package registries
# - Homebrew tap (automated)
# - Docker Hub (automated)
# - GitHub Packages (automated)
```

### 8. Update Documentation Site

```bash
# Deploy documentation
cd docs-site
npm run build
npm run deploy

# Update version selector
./scripts/add-version.sh v0.4.0
```

---

## Post-Release Tasks

### Immediate (Within 24 hours)

- [ ] Monitor release metrics and downloads
- [ ] Check CI/CD pipelines for failures
- [ ] Review community feedback on Slack/GitHub
- [ ] Update project board and close completed issues
- [ ] Announce release on social media

### Short-term (Within 1 week)

- [ ] Monitor bug reports and issues
- [ ] Prepare hotfix if critical issues found
- [ ] Update roadmap based on release
- [ ] Collect user feedback
- [ ] Update marketing materials

### Medium-term (Within 1 month)

- [ ] Analyze adoption metrics
- [ ] Plan next release features
- [ ] Address technical debt identified
- [ ] Review and update processes
- [ ] Celebrate with team! 🎉

---

## Hotfix Process

### When to Create a Hotfix

- Critical security vulnerability discovered
- Production-breaking bug
- Data loss or corruption issue
- Service unavailability

### Hotfix Steps

```bash
# 1. Create hotfix branch from release tag
git checkout v0.3.8
git checkout -b hotfix/v0.3.9

# 2. Fix the issue
# ... make necessary changes ...

# 3. Test thoroughly
make test
./scripts/test-providers.sh

# 4. Update CHANGELOG
cat >> CHANGELOG.md << 'EOF'
## [0.3.9] - 2025-11-21

### Fixed
- Critical bug in X causing Y
EOF

# 5. Commit and tag
git add .
git commit -m "fix: critical issue in component X"
git tag -a v0.3.9 -m "Hotfix v0.3.9"

# 6. Merge to main and develop
git checkout main
git merge hotfix/v0.3.9
git push origin main
git push origin v0.3.9

# 7. Release
goreleaser release --clean
```

### Hotfix Communication

- Update SECURITY.md if security-related
- Post immediate notification on Slack
- Send email to mailing list
- Create GitHub security advisory
- Update status page

---

## Rollback Procedures

### Identifying Need for Rollback

Monitor these indicators:
- Error rates spike (>5%)
- Performance degradation (>20% slower)
- Multiple critical bug reports
- Service unavailability
- Security breach

### Rollback Steps

```bash
# 1. Assess the situation
adhar version  # Check current version
adhar get status  # Check platform health

# 2. Revert to previous version
# Option A: Downgrade binary
curl -fsSL https://github.com/adhar-io/adhar/releases/download/v0.3.8/adhar-linux-amd64 -o adhar
chmod +x adhar

# Option B: Use backup cluster
adhar cluster switch --to backup-cluster

# 3. Rollback platform services
kubectl apply -f manifests/v0.3.8/

# 4. Verify rollback
adhar get status
adhar health check

# 5. Communicate rollback
# - Post incident report
# - Update status page
# - Notify users
```

### Post-Rollback

- [ ] Investigate root cause
- [ ] Document lessons learned
- [ ] Fix issues before next release
- [ ] Update testing procedures
- [ ] Review release process

---

## Release Schedule

### Regular Releases

| Type | Frequency | Day | Time (UTC) |
|------|-----------|-----|------------|
| Major | 6-12 months | Tuesday | 14:00 |
| Minor | 1-2 months | Tuesday | 14:00 |
| Patch | As needed | Tuesday | 14:00 |
| Hotfix | Immediate | Any day | Any time |

### Release Windows

- **Regular releases**: Tuesday 14:00 UTC
- **Avoid**: Fridays, weekends, holidays
- **Best time**: Mid-week, business hours

### Communication Timeline

| Time | Action |
|------|--------|
| T-7 days | Release candidate published |
| T-3 days | Release notes preview |
| T-1 day | Final testing, last changes |
| T-0 | Release published |
| T+1 hour | Social media announcement |
| T+24 hours | Post-release review |

---

## Automation

### CI/CD Pipeline

Our release process is automated using GitHub Actions:

```yaml
# .github/workflows/release.yml
name: Release

on:
  push:
    tags:
      - 'v*'

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v4
        with:
          go-version: '1.23'
      
      # Run tests
      - run: make test
      
      # Build and release
      - uses: goreleaser/goreleaser-action@v5
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
      
      # Build and push Docker images
      - uses: docker/build-push-action@v5
        with:
          push: true
          tags: adhar/adhar:${{ github.ref_name }}
```

---

## Release Checklist Template

```markdown
## Release v0.x.x Checklist

### Pre-Release
- [ ] Version updated in all files
- [ ] CHANGELOG.md updated
- [ ] Tests passing
- [ ] Security scan clean
- [ ] Documentation updated
- [ ] Migration guide (if needed)

### Release
- [ ] Release branch created
- [ ] PR created and approved
- [ ] Tag created
- [ ] Binaries built
- [ ] Docker images published
- [ ] Release notes published

### Post-Release
- [ ] Announcement posted
- [ ] Documentation deployed
- [ ] Monitoring active
- [ ] Community notified
- [ ] Roadmap updated

### Sign-off
- [ ] Release Manager: @username
- [ ] Technical Lead: @username
- [ ] QA Lead: @username
```

---

## Additional Resources

- **[Contributing Guide](../CONTRIBUTING.md)** - How to contribute
- **[Security Policy](../SECURITY.md)** - Security reporting
- **[Changelog](../CHANGELOG.md)** - Version history
- **[Roadmap](ROADMAP.md)** - Future plans

---

## Support

For questions about releases:
- **Slack**: #releases channel
- **Email**: releases@adhar.io
- **GitHub**: Open an issue with `release` label

---

**Happy Releasing! 🚀**

