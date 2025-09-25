# Provider Configuration

Place provider configuration manifests in this directory.  Examples include:

- `providerconfigs/<provider>.yaml` for `ProviderConfig` resources
- Credential secret templates for different environments
- Opinionated guardrails, such as service quotas or tagging policies

These files are packaged with the configuration so that installing the control plane can bootstrap default provider connectivity.
