# nginx-unprivileged runs as a non-root user (uid 101) and listens on 8080 —
# matching the manifests' securityContext (runAsNonRoot, drop ALL,
# RuntimeDefault seccomp). Swap the COPY of site/ for your framework's build
# output (e.g. a `node` build stage emitting dist/) as the app grows.
FROM docker.io/nginxinc/nginx-unprivileged:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY site/ /usr/share/nginx/html/
EXPOSE 8080
