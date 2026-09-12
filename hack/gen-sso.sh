#!/usr/bin/env bash
#
# gen-sso.sh — put a platform UI behind Keycloak SSO (oauth2-proxy front).
#
# Usage:
#   hack/gen-sso.sh <package-dir> <app> <upstream-service> <upstream-port> [extra oauth2-proxy args...]
#
#   package-dir       package directory, e.g. platform/stack/packages/data/trino
#   app               short app id: the client id AND the hostname label
#                     (<app>.adhar.localtest.me), e.g. trino
#   upstream-service  Service (in adhar-system) the current HTTPRoute points at
#   upstream-port     its port
#   extra args        appended to the oauth2-proxy container args, e.g. more
#                     upstreams for multi-backend routes:
#                       --upstream=http://oncall-grafana.adhar-system.svc.cluster.local:80/grafana
#
#                     IMPORTANT -- oauth2-proxy mount-path semantics (v7,
#                     pkg/upstream/proxy.go registerHandler): a mount path that
#                     ends in '/' is registered with mux PathPrefix and matches
#                     everything below it; a mount path WITHOUT a trailing slash
#                     is registered with mux Path and matches that ONE path
#                     exactly. So '--upstream=http://svc:8000/api' does not serve
#                     /api/instances/ -- that request falls through to the '/'
#                     catch-all (the app's web UI), which silently breaks every
#                     API call behind the proxy. This script therefore appends a
#                     trailing slash to every non-root upstream path; requests to
#                     the bare path are 301'd to the slashed form by
#                     oauth2-proxy's trailing-slash handler. Upstreams are sorted
#                     by longest path internally, so the order here is cosmetic;
#                     the request path is forwarded unchanged (the upstream URL's
#                     own path is cleared).
#
# Environment knobs:
#   SSO_BEARER=1      machine-facing endpoint (ingestion APIs such as loki /
#                     tempo / mimir): also accept Keycloak-issued bearer access
#                     tokens (--skip-jwt-bearer-tokens) and enable the client's
#                     service account + a self-audience mapper, so a spoke can
#                     obtain a token via client_credentials with the client id
#                     + <PREFIX>_CLIENT_SECRET from keycloak-clients.
#
# Writes <package-dir>/manifests/sso.yaml containing, mirroring
# application/nexus/manifests/dashboard-sso.yaml:
#   - ConfigMap  <app>-keycloak-client   labelled adhar.io/keycloak-client=true,
#                                         data.client.json (Keycloak client payload);
#                                         the keycloak package's config Job /
#                                         5-minute CronJob provisions the client and
#                                         exports <PREFIX>_CLIENT_SECRET into the
#                                         keycloak-clients Secret (PREFIX = APP with
#                                         '-' -> '_' upper-cased)
#   - Password   <app>-oauth2-cookie     (ESO generator: cookie secret)
#   - ExternalSecret <app>-oauth2-proxy   client secret + cookie secret
#   - Deployment/Service <app>-oauth2-proxy  on port 4180 (sync-wave 5)
#
# Then point the package's HTTPRoute backendRefs at <app>-oauth2-proxy:4180
# and give the route argocd.argoproj.io/sync-wave: "6" (it MUST sync after the
# proxy: a route whose backend Service does not exist yet is Degraded and
# deadlocks the sync).
#
# Host convention: manifests use https://<app>.adhar.localtest.me:8443; the
# platform controller rewrites adhar.localtest.me:8443 to the real domain when
# seeding cloud clusters. Never hardcode another domain or port.
#
# Examples:
#   hack/gen-sso.sh platform/stack/packages/data/trino trino trino 8080
#   hack/gen-sso.sh platform/stack/packages/observability/oncall oncall oncall-engine 8080 \
#       --upstream=http://oncall-grafana.adhar-system.svc.cluster.local:80/grafana
#   SSO_BEARER=1 hack/gen-sso.sh platform/stack/packages/observability/loki-stack loki loki 3100
set -euo pipefail

if [[ $# -lt 4 ]]; then
  sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
  exit 2
fi

PKG_DIR="$1"; APP="$2"; UPSTREAM_SVC="$3"; UPSTREAM_PORT="$4"; shift 4
EXTRA_ARGS=("$@")
BEARER="${SSO_BEARER:-0}"

if [[ ! "${APP}" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]]; then
  echo "app id must be a DNS label (lowercase alnum + '-'): ${APP}" >&2
  exit 2
fi
if [[ ! -d "${PKG_DIR}/manifests" ]]; then
  echo "no manifests/ directory under ${PKG_DIR}" >&2
  exit 2
fi

OUT="${PKG_DIR}/manifests/sso.yaml"
HOST="${APP}.adhar.localtest.me"
PREFIX="$(printf '%s' "${APP}" | tr '[:lower:]' '[:upper:]' | tr '-' '_')"
TITLE="$(printf '%s' "${APP}" | awk -F- '{for(i=1;i<=NF;i++){$i=toupper(substr($i,1,1)) substr($i,2)}; print}' OFS=' ')"
UPSTREAM_URL="http://${UPSTREAM_SVC}.adhar-system.svc.cluster.local:${UPSTREAM_PORT}"

# --- Keycloak client payload -------------------------------------------------
CLIENT_SA=false
CLIENT_MAPPERS=""
BEARER_ARGS=""
BEARER_NOTE=""
REGEN_ENV=""
if [[ "${BEARER}" == "1" ]]; then
  CLIENT_SA=true
  REGEN_ENV="SSO_BEARER=1 "
  # Self-audience mapper: oauth2-proxy verifies a bearer token's aud against
  # the client id, and Keycloak client_credentials tokens do not carry it
  # by default.
  CLIENT_MAPPERS=',
      "protocolMappers": [
        {
          "name": "self-audience",
          "protocol": "openid-connect",
          "protocolMapper": "oidc-audience-mapper",
          "config": {
            "included.custom.audience": "'"${APP}"'",
            "id.token.claim": "false",
            "access.token.claim": "true"
          }
        }
      ]'
  BEARER_ARGS='
            # Machine clients (workload-cluster spokes) authenticate with a
            # Keycloak bearer access token instead of the browser login flow:
            #   POST https://keycloak.<host>/realms/adhar/protocol/openid-connect/token
            #        grant_type=client_credentials client_id='"${APP}"' client_secret=<'"${PREFIX}"'_CLIENT_SECRET>
            - --skip-jwt-bearer-tokens=true'
  BEARER_NOTE="
# Machine-facing (SSO_BEARER=1): besides the browser login, requests carrying a
# Keycloak-issued bearer access token for the \`${APP}\` client (client_credentials
# with ${PREFIX}_CLIENT_SECRET from keycloak-clients) pass straight through."
fi

# Normalise upstream mount paths: oauth2-proxy only prefix-matches a path that
# ends in '/' (see the header note). A non-root upstream without one is an exact
# match and everything below it silently lands on the '/' catch-all.
EXTRA_YAML=""
for a in "${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"}"; do
  if [[ "${a}" == --upstream=* ]]; then
    u="${a#--upstream=}"
    # path = whatever follows scheme://host[:port]
    rest="${u#*://}"
    path="/${rest#*/}"
    if [[ "${rest}" == */* && "${path}" != "/" && "${path}" != */ ]]; then
      a="--upstream=${u}/"
    fi
  fi
  EXTRA_YAML+=$'\n'"            - ${a}"
done

cat > "${OUT}" <<EOF
# Keycloak SSO in front of ${TITLE} (same pattern as Nexus/Tekton/Harbor): the
# UI is gated by an oauth2-proxy using the \`${APP}\` Keycloak client, which is
# provisioned generically from the labelled ConfigMap below by the keycloak
# package (config Job + 5-minute client-reconcile CronJob) and whose secret
# arrives via ESO from keycloak-clients (${PREFIX}_CLIENT_SECRET).
# The oidc-issuer-url auto-discovers Keycloak's endpoints via
# /.well-known/openid-configuration. The HTTPRoute (sync-wave 6) points at
# ${APP}-oauth2-proxy:4180; the proxy forwards to ${UPSTREAM_URL}.${BEARER_NOTE}
#
# GENERATED by hack/gen-sso.sh — regenerate rather than hand-edit:
#   ${REGEN_ENV}hack/gen-sso.sh ${PKG_DIR} ${APP} ${UPSTREAM_SVC} ${UPSTREAM_PORT}${EXTRA_ARGS[@]+ ${EXTRA_ARGS[*]}}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${APP}-keycloak-client
  namespace: adhar-system
  labels:
    # Picked up by the keycloak package's client reconciler (see
    # security/keycloak/manifests/keycloak-config.yaml, configure.sh).
    adhar.io/keycloak-client: "true"
    app.kubernetes.io/name: ${APP}-keycloak-client
    app.kubernetes.io/part-of: ${APP}
  # Wave 0: a ConfigMap is healthy instantly and blocks nothing; the earlier
  # it exists, the sooner the reconciler exports the client secret the
  # wave-5 proxy waits on.
  annotations: {argocd.argoproj.io/sync-wave: "0"}
data:
  # Keep "description" under 255 chars (Keycloak varchar; longer fails with 500).
  client.json: |
    {
      "protocol": "openid-connect",
      "clientId": "${APP}",
      "name": "${TITLE} Client",
      "description": "Used for ${TITLE} SSO via oauth2-proxy",
      "publicClient": false,
      "authorizationServicesEnabled": false,
      "serviceAccountsEnabled": ${CLIENT_SA},
      "implicitFlowEnabled": false,
      "directAccessGrantsEnabled": true,
      "standardFlowEnabled": true,
      "frontchannelLogout": true,
      "redirectUris": ["https://${HOST}:8443/oauth2/callback"],
      "webOrigins": ["https://${HOST}:8443"]${CLIENT_MAPPERS}
    }
---
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata:
  name: ${APP}-oauth2-cookie
  namespace: adhar-system
  # Wave 5: the SSO front-end deploys AFTER the app core (wave 0-3). Otherwise
  # this oauth2-proxy — which waits on a Keycloak-provisioned ExternalSecret —
  # sits unhealthy at wave 0 and blocks the core from ever deploying.
  annotations: {argocd.argoproj.io/sync-wave: "5"}
spec:
  length: 32
  digits: 8
  symbols: 0
  noUpper: false
  allowRepeat: true
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: ${APP}-oauth2-proxy
  namespace: adhar-system
  annotations: {argocd.argoproj.io/sync-wave: "5"}
spec:
  refreshPolicy: CreatedOnce
  refreshInterval: 5m
  secretStoreRef:
    name: keycloak
    kind: ClusterSecretStore
  target:
    name: ${APP}-oauth2-proxy
    template:
      engineVersion: v2
      data:
        OAUTH2_PROXY_CLIENT_SECRET: "{{ .${PREFIX}_CLIENT_SECRET }}"
        OAUTH2_PROXY_COOKIE_SECRET: "{{ .COOKIE_SECRET }}"
  dataFrom:
    - sourceRef:
        generatorRef:
          apiVersion: generators.external-secrets.io/v1alpha1
          kind: Password
          name: ${APP}-oauth2-cookie
      rewrite:
        - transform:
            template: "COOKIE_SECRET"
  data:
    - secretKey: ${PREFIX}_CLIENT_SECRET
      remoteRef:
        key: keycloak-clients
        property: ${PREFIX}_CLIENT_SECRET
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${APP}-oauth2-proxy
  namespace: adhar-system
  annotations: {argocd.argoproj.io/sync-wave: "5"}
  labels:
    app.kubernetes.io/name: ${APP}-oauth2-proxy
    app.kubernetes.io/part-of: ${APP}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${APP}-oauth2-proxy
  template:
    metadata:
      labels:
        app: ${APP}-oauth2-proxy
    spec:
      enableServiceLinks: false
      containers:
        - name: oauth2-proxy
          image: quay.io/oauth2-proxy/oauth2-proxy:v7.7.1
          args:
            - --http-address=0.0.0.0:4180
            - --provider=oidc
            - --oidc-issuer-url=https://keycloak.adhar.localtest.me:8443/realms/adhar
            - --ssl-insecure-skip-verify=true
            - --client-id=${APP}
            - --redirect-url=https://${HOST}:8443/oauth2/callback
            - --upstream=${UPSTREAM_URL}${EXTRA_YAML}
            - --email-domain=*
            - --scope=openid profile email groups
            - --cookie-secure=true
            - --skip-provider-button=true
            - --reverse-proxy=true
            # Forward the authenticated identity (X-Forwarded-User/Email/
            # Preferred-Username) so apps with header/proxy auth can use it.
            - --pass-user-headers=true${BEARER_ARGS}
          envFrom:
            - secretRef:
                name: ${APP}-oauth2-proxy
          ports:
            - name: http
              containerPort: 4180
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
          readinessProbe:
            httpGet:
              path: /ping
              port: 4180
            initialDelaySeconds: 5
            timeoutSeconds: 10
            failureThreshold: 5
---
apiVersion: v1
kind: Service
metadata:
  name: ${APP}-oauth2-proxy
  namespace: adhar-system
  annotations: {argocd.argoproj.io/sync-wave: "5"}
  labels:
    app.kubernetes.io/name: ${APP}-oauth2-proxy
    app.kubernetes.io/part-of: ${APP}
spec:
  selector:
    app: ${APP}-oauth2-proxy
  ports:
    - name: http
      port: 4180
      targetPort: 4180
EOF

echo "wrote ${OUT} (client ${APP} -> ${PREFIX}_CLIENT_SECRET, upstream ${UPSTREAM_URL})"
