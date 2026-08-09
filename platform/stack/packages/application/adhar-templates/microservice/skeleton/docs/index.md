# Golden Path: Go Microservice

A production-quality starting point scaffolded by the Adhar platform:

- **Service** — `main.go`: plain `net/http`, `/healthz` (liveness) and
  `/readyz` (readiness) endpoints, JSON logs, graceful shutdown that fails
  readiness before draining so rolling deploys drop no requests.
- **Image** — multi-stage `Dockerfile` producing a static binary on
  distroless (`nonroot`, no shell).
- **Deployment** — `manifests/`: Deployment with probes, resource
  requests/limits, and a hardened securityContext (runAsNonRoot, drop ALL,
  RuntimeDefault seccomp), a Service, and an HTTPRoute through the platform
  Gateway. The Deployment starts on a placeholder image (podinfo, same
  endpoint contract) until your first release is promoted.
- **CI** — `jenkins-x.yml` + `.lighthouse/triggers.yaml` (ADR-0018): PRs run
  the platform `adhar-pr-verify` pipeline, merges to `main` run
  `adhar-release`, which ends by opening a promotion PR against the
  environments repo. Add `ci/test.sh` to customize the test step.

Once synced by ArgoCD, the service answers at
`https://<name>.adhar.localtest.me:8443`.

### adhar

Checkout adhar website: https://adhar.io

Checkout adhar repository: https://github.com/adhar-io/adhar
