#!/usr/bin/env bash

set -euo pipefail

repository_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
exec "$repository_root/services/omai-control-plane/scripts/live-audit-linux.sh" \
  --require-platform "$@"
