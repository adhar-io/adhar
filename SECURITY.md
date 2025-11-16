# Security Policy

## 🔒 Security Overview

The Adhar Platform takes security seriously. We appreciate the security community's efforts in helping us maintain the security of our platform and protect our users.

---

## 📢 Supported Versions

We actively support the following versions with security updates:

| Version | Supported          | End of Support |
| ------- | ------------------ | -------------- |
| 0.3.x   | ✅ Yes             | TBD            |
| 0.2.x   | ⚠️ Limited         | 2025-12-31     |
| < 0.2   | ❌ No              | Ended          |

**Recommendation**: Always use the latest stable release for the best security posture.

---

## 🚨 Reporting a Vulnerability

### Where to Report

**DO NOT** open a public GitHub issue for security vulnerabilities.

Please report security vulnerabilities through one of these channels:

1. **GitHub Security Advisory** (Preferred)
   - Navigate to the [Security tab](https://github.com/adhar-io/adhar/security/advisories)
   - Click "Report a vulnerability"
   - Fill out the form with vulnerability details

2. **Email**
   - Send to: security@adhar.io
   - Use PGP key: [Download our PGP key](https://adhar.io/pgp-key.asc)
   - Subject: "SECURITY: [Brief Description]"

3. **Private Disclosure via Slack**
   - DM to @security-team on [Adhar Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)
   - Only for initial contact; detailed info should follow via email

### What to Include

Please provide as much information as possible:

- **Vulnerability Type**: What kind of vulnerability is it? (e.g., XSS, SQL injection, privilege escalation)
- **Affected Component**: Which part of Adhar is affected?
- **Affected Versions**: Which versions are vulnerable?
- **Attack Vector**: How can the vulnerability be exploited?
- **Impact**: What is the potential impact?
- **Proof of Concept**: Steps to reproduce or PoC code (if available)
- **Suggested Fix**: If you have ideas on how to fix it
- **Your Contact Info**: For follow-up questions

### Example Report Template

```markdown
## Vulnerability Report

**Title**: Brief description of the vulnerability

**Affected Component**: 
- Component: [e.g., CLI, Provider, Controller]
- File/Function: [specific location if known]

**Versions Affected**: 
- Version: [e.g., v0.3.0 - v0.3.8]

**Severity**: [Critical/High/Medium/Low]

**Vulnerability Type**: [e.g., Authentication bypass, Code injection]

**Description**:
[Detailed description of the vulnerability]

**Impact**:
[What an attacker could do with this vulnerability]

**Steps to Reproduce**:
1. Step 1
2. Step 2
3. ...

**Proof of Concept**:
```bash
# PoC code or commands
```

**Suggested Mitigation**:
[Your suggestions if any]

**Additional Context**:
[Any other relevant information]
```

---

## 🔄 Response Timeline

We are committed to responding quickly to security reports:

| Timeframe | Action |
|-----------|--------|
| **24 hours** | Initial acknowledgment of your report |
| **48 hours** | Preliminary assessment and severity rating |
| **7 days** | Detailed response with our action plan |
| **30 days** | Target fix release (may vary by severity) |
| **90 days** | Public disclosure (coordinated with reporter) |

### Severity Ratings

We use CVSS v3.1 for severity ratings:

- **Critical (9.0-10.0)**: Immediate action, patch within 7 days
- **High (7.0-8.9)**: Urgent, patch within 14 days
- **Medium (4.0-6.9)**: Important, patch within 30 days
- **Low (0.1-3.9)**: Informational, patch in next regular release

---

## 🎯 Security Best Practices

### For Users

#### Secure Installation

```bash
# Always verify the installation script
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/scripts/install.sh -o install.sh
cat install.sh  # Review before running
bash install.sh

# Verify binary authenticity
adhar version --verify
```

#### Configuration Security

```yaml
# Use environment variables for secrets
providers:
  aws:
    useEnvironment: true  # Use AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
    
# Never commit credentials to Git
# Add to .gitignore:
# *-config.yaml
# .env
# *.key
# *.pem
```

#### Network Security

```bash
# Enable network policies
adhar up --enable-network-policies

# Use private clusters for production
adhar cluster create prod \
  --provider gcp \
  --private-cluster \
  --authorized-networks "YOUR_IP/32"
```

#### Access Control

```bash
# Enable RBAC
adhar up --enable-rbac

# Use least privilege principle
kubectl create serviceaccount limited-user
kubectl create rolebinding limited-user \
  --clusterrole=view \
  --serviceaccount=default:limited-user
```

### For Developers

#### Secure Development

```bash
# Run security scan before committing
make security-scan

# Check for vulnerable dependencies
go list -json -m all | nancy sleuth

# Scan Docker images
trivy image adhar:latest
```

#### Code Review Checklist

- [ ] No hardcoded secrets or credentials
- [ ] Input validation on all user inputs
- [ ] Authentication and authorization checks
- [ ] Secure default configurations
- [ ] Error messages don't leak sensitive info
- [ ] Dependencies are up-to-date
- [ ] Security tests added for new features

---

## 🛡️ Security Features

### Built-in Security

Adhar includes security features by default:

#### Network Security
- **Cilium CNI**: eBPF-based network security
- **Network Policies**: Micro-segmentation enabled
- **Encryption**: WireGuard encryption for pod-to-pod traffic
- **Zero-Trust**: Default deny network policies

#### Secrets Management
- **HashiCorp Vault**: Enterprise secrets management
- **External Secrets Operator**: Kubernetes secrets sync
- **Encryption at Rest**: etcd encryption enabled
- **RBAC**: Role-based access control

#### Policy Enforcement
- **Kyverno**: Policy engine for Kubernetes
- **OPA Gatekeeper**: Open Policy Agent integration
- **Pod Security Standards**: Enforce security standards
- **Admission Controllers**: Validate and mutate resources

#### Scanning & Monitoring
- **Trivy**: Vulnerability scanning
- **Falco**: Runtime security monitoring
- **Prometheus**: Security metrics
- **Audit Logging**: Complete audit trail

---

## 🔍 Known Security Considerations

### Local Development (Kind)

- Kind clusters are for **development only**
- Default passwords are used (change in production)
- Network policies may be relaxed
- Do not use for sensitive workloads

### Cloud Provider Credentials

- Never commit credentials to version control
- Use IAM roles/service accounts when possible
- Rotate credentials regularly
- Use least privilege principle
- Enable MFA on cloud provider accounts

### Platform Services

- Default admin credentials should be changed immediately
- Enable TLS for all services in production
- Restrict ingress access with IP whitelisting
- Regular backup and disaster recovery testing

---

## 📋 Security Checklist for Production

Before deploying to production:

### Infrastructure
- [ ] Private cluster/network configuration
- [ ] Network policies enabled
- [ ] Encryption at rest enabled
- [ ] Encryption in transit enabled
- [ ] Firewall rules configured
- [ ] IP whitelisting configured

### Authentication & Authorization
- [ ] Default passwords changed
- [ ] RBAC policies configured
- [ ] Service accounts use least privilege
- [ ] MFA enabled for admin accounts
- [ ] SSO/OIDC integration configured

### Secrets Management
- [ ] Vault or external secrets manager configured
- [ ] Secrets encrypted at rest
- [ ] Secrets rotation policy implemented
- [ ] No secrets in Git repositories
- [ ] No secrets in container images

### Monitoring & Auditing
- [ ] Security monitoring enabled
- [ ] Audit logging enabled
- [ ] Log retention policy configured
- [ ] Alerting configured
- [ ] Incident response plan documented

### Compliance
- [ ] Security policies defined and enforced
- [ ] Vulnerability scanning automated
- [ ] Regular security audits scheduled
- [ ] Compliance requirements met
- [ ] Data privacy requirements addressed

---

## 🆘 Security Incident Response

### If You Suspect a Breach

1. **Isolate**: Immediately isolate affected components
2. **Report**: Contact security@adhar.io
3. **Preserve**: Preserve logs and evidence
4. **Document**: Document timeline and actions taken
5. **Communicate**: Follow our communication plan

### Our Incident Response

1. **Acknowledge**: Within 1 hour of report
2. **Assess**: Severity and impact analysis
3. **Contain**: Stop the breach if ongoing
4. **Remediate**: Apply fixes and patches
5. **Communicate**: Notify affected users
6. **Post-Mortem**: Document and learn

---

## 📚 Security Resources

### Documentation
- [User Guide - Security](docs/USER_GUIDE.md#security--compliance)
- [Advanced Guide - Security Hardening](docs/ADVANCED.md#security-hardening)
- [Architecture - Security](docs/ARCHITECTURE.md)

### External Resources
- [Kubernetes Security Best Practices](https://kubernetes.io/docs/concepts/security/)
- [CIS Kubernetes Benchmark](https://www.cisecurity.org/benchmark/kubernetes)
- [OWASP Kubernetes Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Kubernetes_Security_Cheat_Sheet.html)
- [Cloud Security Alliance](https://cloudsecurityalliance.org/)

### Security Tools Used
- [Trivy](https://github.com/aquasecurity/trivy) - Vulnerability scanner
- [Falco](https://falco.org/) - Runtime security
- [Kyverno](https://kyverno.io/) - Policy engine
- [Vault](https://www.vaultproject.io/) - Secrets management

---

## 🏆 Security Hall of Fame

We recognize and thank security researchers who have helped improve Adhar's security:

<!-- 
Add researchers here who have responsibly disclosed vulnerabilities
Format: - **Name** - Description of finding - Date
-->

*No vulnerabilities have been publicly disclosed yet.*

---

## 📞 Contact

- **Security Email**: security@adhar.io
- **PGP Key**: [Download](https://adhar.io/pgp-key.asc)
- **Security Team**: @security-team on [Slack](https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww)

---

## 📄 Legal

This security policy is subject to our [Terms of Service](https://adhar.io/terms) and [Privacy Policy](https://adhar.io/privacy).

**Last Updated**: November 2025  
**Version**: 1.0

---

**Thank you for helping keep Adhar and our users safe!** 🙏

