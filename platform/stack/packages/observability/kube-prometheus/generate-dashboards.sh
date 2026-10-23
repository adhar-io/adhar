#!/usr/bin/env bash
# Every Adhar dashboard is BUILT: run ./build-service-dashboards.py (the folder
# dashboards) and ./build-platform-dashboard.py (the front page). This wrapper is
# kept for generate-dashboards.py, whose DASHBOARDS download list is empty on
# purpose (see its docstring).
set -euo pipefail
cd "$(dirname "$0")"
exec python3 generate-dashboards.py "$@"
