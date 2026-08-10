#!/usr/bin/env bash
# Regenerates the in-repo Grafana dashboard ConfigMaps for the two-folder
# layout (Platform / Application). Each community dashboard is downloaded ONCE
# from grafana.com, its datasource inputs are pinned to the platform's
# provisioned datasource UIDs, and it is wrapped in a sidecar ConfigMap
# (label grafana_dashboard=1, annotation grafana_folder=<folder>) written to
# manifests/. Runtime therefore needs NO egress to grafana.com — the JSON lives
# in the repo. Re-run to refresh to the latest revisions.
#
#   Usage: ./generate-dashboards.sh
#
# Add/remove dashboards by editing the DASHBOARDS list inside the script.
set -euo pipefail
cd "$(dirname "$0")"
exec python3 generate-dashboards.py "$@"
