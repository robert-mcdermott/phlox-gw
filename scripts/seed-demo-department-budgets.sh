#!/usr/bin/env bash
set -euo pipefail

db_path="${1:-phlox-gw.db}"

go run ./scripts/seed-demo-data.go -db "${db_path}" -budget-mode department -yes
