#!/usr/bin/env bash

set -euo pipefail

repository_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
audit_image=${OMAI_E2E_AUDIT_IMAGE:-omai/e2e-audit:go1.26.6}
stamp=$(date -u +%Y%m%dT%H%M%SZ)
run_name="$stamp-$$"
results_root="$repository_root/e2e-results"
run_dir="$results_root/$run_name"
project="omai-e2e-${run_name,,}"
network="$project-audit"
redis_container="$project-redis"
secret_file=""
compose_started=0
network_created=0

usage() {
  cat <<'EOF'
OMAI complete Docker E2E

Usage:
  ./RUN_E2E_DOCKER.sh [live-audit options]

Requirements:
  Linux, Bash, Docker Engine and Docker Compose v2. The audit image contains
  the pinned Go, Buf, Node, Bun, security scanners, grpcurl, Chromium and all
  project dependencies.

Examples:
  ./RUN_E2E_DOCKER.sh
  ./RUN_E2E_DOCKER.sh --requests 10000 --concurrency 128
  ./RUN_E2E_DOCKER.sh --real-deepseek-acp --deepseek-model deepseek-v4-flash

For the paid provider probe, export DEEPSEEK_API_KEY, set
DEEPSEEK_API_KEY_FILE, or enter the key at the hidden prompt. The key is copied
to a mode-0600 temporary file, mounted read-only, never placed in an image,
argument, report or Compose environment, and removed on exit.
EOF
}

real_provider=0
for argument in "$@"; do
  case "$argument" in
    --real-deepseek-acp)
      real_provider=1
      ;;
    --bootstrap|--docker|--allow-missing-tools|--output)
      echo "$argument is managed by RUN_E2E_DOCKER.sh and must not be supplied" >&2
      exit 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
  esac
done

command -v docker >/dev/null || {
  echo "Docker Engine is required" >&2
  exit 2
}
docker compose version >/dev/null || {
  echo "Docker Compose v2 is required" >&2
  exit 2
}
[[ $(uname -s) == Linux ]] || {
  echo "the complete E2E runner is intentionally Linux-only" >&2
  exit 2
}

mkdir -p "$run_dir" "$run_dir/workspaces"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if ((compose_started == 1)); then
    (cd "$repository_root" && docker compose --project-name "$project" down --volumes --remove-orphans) >/dev/null 2>&1 || true
  fi
  docker rm --force "$redis_container" >/dev/null 2>&1 || true
  if ((network_created == 1)); then
    docker network rm "$network" >/dev/null 2>&1 || true
  fi
  if [[ -n $secret_file && -f $secret_file ]]; then
    if command -v shred >/dev/null; then
      shred --remove "$secret_file"
    else
      rm -f -- "$secret_file"
    fi
  fi
  if docker image inspect "$audit_image" >/dev/null 2>&1; then
    docker run --rm --volume "$results_root:/evidence" --entrypoint chown "$audit_image" \
      -R "$(id -u):$(id -g)" "/evidence/$run_name" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ((real_provider == 1)); then
  umask 077
  secret_file=$(mktemp "${TMPDIR:-/tmp}/omai-deepseek.XXXXXX")
  if [[ -n ${DEEPSEEK_API_KEY-} ]]; then
    printf '%s' "$DEEPSEEK_API_KEY" > "$secret_file"
    unset DEEPSEEK_API_KEY
  elif [[ -n ${DEEPSEEK_API_KEY_FILE-} ]]; then
    if [[ ! -f $DEEPSEEK_API_KEY_FILE || -L $DEEPSEEK_API_KEY_FILE || ! -r $DEEPSEEK_API_KEY_FILE ]]; then
      echo "DEEPSEEK_API_KEY_FILE must be a readable regular file, not a symlink" >&2
      exit 2
    fi
    cp -- "$DEEPSEEK_API_KEY_FILE" "$secret_file"
  else
    read -rsp 'DeepSeek API key: ' provider_key
    printf '\n'
    printf '%s' "$provider_key" > "$secret_file"
    unset provider_key
  fi
  chmod 600 "$secret_file"
fi

echo "[1/6] Building the pinned audit toolchain and dependency image"
DOCKER_BUILDKIT=1 docker build --pull --file "$repository_root/Dockerfile.e2e" \
  --tag "$audit_image" "$repository_root" 2>&1 | tee "$run_dir/audit-image-build.log"
docker image inspect "$audit_image" > "$run_dir/audit-image.json"
docker run --rm --volume "$run_dir/workspaces:/workspace" --entrypoint chown \
  "$audit_image" -R 65532:65532 /workspace

echo "[2/6] Starting an isolated Redis instance for repository and restart tests"
docker network create "$network" >/dev/null
network_created=1
docker run --detach --name "$redis_container" --network "$network" \
  --user redis \
  --read-only --tmpfs /data:size=256m,mode=1777 \
  --security-opt no-new-privileges:true --cap-drop ALL \
  redis:8.2.8-alpine redis-server --appendonly yes --appendfsync everysec \
  --maxmemory 192mb --maxmemory-policy noeviction >/dev/null
redis_ready=0
for _ in $(seq 1 60); do
  if docker exec "$redis_container" redis-cli ping 2>/dev/null | grep -qx PONG; then
    redis_ready=1
    break
  fi
  sleep 0.25
done
if ((redis_ready == 0)); then
  docker logs "$redis_container" >&2 || true
  echo "Redis did not become healthy" >&2
  exit 1
fi

echo "[3/6] Running source, race, security, SDK, Portal, harness and live API gates"
audit_run=(
  docker run --rm --init --network "$network"
  --volume "$results_root:/evidence"
  --env "OMAI_REDIS_TEST_ADDR=$redis_container:6379"
)
if ((real_provider == 1)); then
  audit_run+=(
    --mount "type=bind,src=$secret_file,dst=/run/secrets/deepseek,readonly"
    --env DEEPSEEK_API_KEY_FILE=/run/secrets/deepseek
  )
fi
audit_run+=("$audit_image" --output "/evidence/$run_name/audit" "$@")
"${audit_run[@]}" 2>&1 | tee "$run_dir/live-audit.log"

random_token() {
  od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
}

port_block_available() {
  local base=$1
  local offset
  for offset in 0 1 2 3 4; do
    if (exec 9<>"/dev/tcp/127.0.0.1/$((base + offset))") 2>/dev/null; then
      exec 9>&-
      return 1
    fi
  done
}

for _ in $(seq 1 100); do
  port_base=$((20000 + RANDOM % 30000))
  if port_block_available "$port_base"; then
    break
  fi
done
if ! port_block_available "$port_base"; then
  echo "could not reserve a free five-port block" >&2
  exit 1
fi

export OMAI_DEV_TOKEN="$(random_token)"
export OMAI_SERVICE_TOKEN="$(random_token)"
export OMAI_ADK_RUNTIME_TOKEN="$(random_token)"
export OMAI_EXECUTOR_TOKEN="$(random_token)"
export OMAI_WORKSPACES_DIR="$run_dir/workspaces"
export OMAI_ENABLE_DEMO_RUNTIME=true
export OMAI_ALLOWED_ORIGINS=http://omai-app
export OMAI_PREVIEW_PUBLIC_BASE_URL=http://omai:8787
export OMAI_PREVIEW_PUBLIC_URL_TEMPLATE=
export GOOGLE_API_KEY=omai-e2e-not-a-provider-credential
export OPENAI_API_KEY=
export OPENROUTER_API_KEY=
export VITE_OMAI_API_BASE_URL=http://omai:8787
export VITE_OMAI_API_TOKEN="$OMAI_DEV_TOKEN"
export VITE_OMAI_VOICE_GATEWAY_URL=ws://voice:8791
export OMAI_API_PORT=$port_base
export OMAI_METRICS_PORT=$((port_base + 1))
export OMAI_FRONTEND_PORT=$((port_base + 2))
export OMAI_VOICE_PORT=$((port_base + 3))
export OMAI_VOICE_METRICS_PORT=$((port_base + 4))

echo "[4/6] Validating and building every production image"
(
  cd "$repository_root"
  docker compose --project-name "$project" config --quiet
  DOCKER_BUILDKIT=1 docker compose --project-name "$project" build --pull
) 2>&1 | tee "$run_dir/production-images.log"

echo "[5/6] Starting the complete stack and running service/browser E2E"
compose_started=1
(
  cd "$repository_root"
  docker compose --project-name "$project" up --detach --no-build --wait --wait-timeout 240
  docker compose --project-name "$project" ps --format json > "$run_dir/compose-services.json"
)
project_network="${project}_default"

probe_environment=(
  --env OMAI_E2E_API_TOKEN="$OMAI_DEV_TOKEN"
  --env OMAI_E2E_SERVICE_TOKEN="$OMAI_SERVICE_TOKEN"
  --env OMAI_E2E_RUNTIME_TOKEN="$OMAI_ADK_RUNTIME_TOKEN"
  --env OMAI_E2E_API_URL=http://omai:8787
  --env OMAI_E2E_GRPC_ADDR=omai:8787
  --env OMAI_E2E_APP_URL=http://omai-app
)
docker run --rm --network "$project_network" --volume "$run_dir:/evidence" \
  "${probe_environment[@]}" \
  --entrypoint /workspace/services/omai-control-plane/scripts/compose-stack-e2e.sh \
  "$audit_image" --output /evidence/compose/create \
  2>&1 | tee "$run_dir/compose-create.log"

docker run --rm --network "$project_network" --volume "$run_dir:/evidence" \
  "${probe_environment[@]}" \
  --env OMAI_E2E_BROWSER_OUTPUT=/evidence/browser \
  --entrypoint node "$audit_image" \
  /workspace/services/omai-control-plane/scripts/portal-browser-smoke.mjs \
  2>&1 | tee "$run_dir/browser.log"

session_id=$(<"$run_dir/compose/create/session-id.txt")
(
  cd "$repository_root"
  docker compose --project-name "$project" restart omai
  docker compose --project-name "$project" up --detach --no-build --wait --wait-timeout 120
)
docker run --rm --network "$project_network" --volume "$run_dir:/evidence" \
  "${probe_environment[@]}" \
  --entrypoint /workspace/services/omai-control-plane/scripts/compose-stack-e2e.sh \
  "$audit_image" --output /evidence/compose/after-restart --verify-session "$session_id" \
  2>&1 | tee "$run_dir/compose-restart.log"

echo "[6/6] Writing the final evidence index"
(
  cd "$repository_root"
  docker compose --project-name "$project" images --format json
) > "$run_dir/production-images.json"
{
  printf '# OMAI complete Docker E2E\n\n'
  printf -- '- Result: **PASS**\n'
  printf -- '- Audit report: `audit/REPORT.md`\n'
  printf -- '- Production images: `production-images.json`\n'
  printf -- '- Compose service state: `compose-services.json`\n'
  printf -- '- Browser evidence: `browser/browser.json` and `browser/portal.png`\n'
  printf -- '- Workspace/Git/preview/voice evidence: `compose/create/`\n'
  printf -- '- Redis restart evidence: `compose/after-restart/messages-after-restart.json`\n'
  if ((real_provider == 1)); then
    printf -- '- Paid provider route: `audit/live/deepseek-acp.json`\n'
  else
    printf -- '- Paid provider route: not requested\n'
  fi
} > "$run_dir/E2E_SUMMARY.md"

echo
echo "OMAI complete E2E: PASS"
echo "Evidence: $run_dir/E2E_SUMMARY.md"
