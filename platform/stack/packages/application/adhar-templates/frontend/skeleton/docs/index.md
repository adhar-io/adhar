# Golden Path: Static Frontend

A production-quality starting point scaffolded by the Adhar platform:

- **Site** — `site/index.html`: replace with your framework's build output
  (the nginx config is SPA-friendly: unknown paths fall back to
  `index.html`).
- **Image** — `Dockerfile` on `nginx-unprivileged` (non-root, listens on
  8080) with a `/healthz` probe endpoint baked into `nginx.conf`.
- **Deployment** — `manifests/`: Deployment with probes, resource
  requests/limits, and a hardened securityContext (runAsNonRoot, drop ALL,
  RuntimeDefault seccomp), a Service, and an HTTPRoute through the platform
  Gateway. The Deployment starts on the stock nginx-unprivileged image until
  your first release is promoted.
- **CI** — `jenkins-x.yml` + `.lighthouse/triggers.yaml` (ADR-0018): PRs run
  the platform `adhar-pr-verify` pipeline, merges to `main` run
  `adhar-release`, which ends by opening a promotion PR against the
  environments repo. Add `ci/test.sh` to customize the test step.

Once synced by ArgoCD, the site answers at
`https://<name>.adhar.localtest.me:8443`.

### adhar

Checkout adhar website: https://adhar.io

Checkout adhar repository: https://github.com/adhar-io/adhar
