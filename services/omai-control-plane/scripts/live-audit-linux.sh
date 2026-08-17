#!/usr/bin/env bash

set -uo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
backend_root=$(cd -- "$script_dir/.." && pwd -P)
platform_root=""
candidate_platform_root=$(cd -- "$backend_root/../.." 2>/dev/null && pwd -P || true)
if [[ -f "$candidate_platform_root/packages/app/package.json" && -f "$candidate_platform_root/package.json" ]]; then
  platform_root=$candidate_platform_root
fi

bootstrap=0
docker_audit_enabled=0
require_platform=0
strict_tools=1
real_deepseek_acp=0
deepseek_model="deepseek-v4-flash"
deepseek_api_key=${DEEPSEEK_API_KEY-}
deepseek_api_key_file=${DEEPSEEK_API_KEY_FILE-}
redis_test_addr=${OMAI_REDIS_TEST_ADDR-}
opencode_test_command=${OMAI_TEST_OPENCODE_COMMAND-}
opencode_test_entry=${OMAI_TEST_OPENCODE_ENTRY-}
unset DEEPSEEK_API_KEY DEEPSEEK_API_KEY_FILE OMAI_REDIS_TEST_ADDR
load_requests=500
load_concurrency=32
output_dir=""

usage() {
  cat <<'EOF'
OMAI Linux live audit

Usage:
  ./scripts/live-audit-linux.sh [options]

Options:
  --bootstrap             Install locked JavaScript dependencies and download Go modules.
  --docker                Validate Compose and build all four production image targets.
  --output DIR            Write the audit report to DIR (must not already exist).
  --requests N            Live ConnectRPC health requests (default: 500).
  --concurrency N         Concurrent live requests (default: 32).
  --require-platform      Fail unless the complete OMAI repository is present.
  --real-deepseek-acp     Run one paid OpenCode ACP -> Go ADK -> DeepSeek API turn.
  --deepseek-model MODEL  DeepSeek model for the paid probe (default: deepseek-v4-flash).
  --allow-missing-tools   Record unavailable optional audit tools instead of failing preflight.
  --help                  Show this help.

Required Linux tools:
  Go >=1.26, Buf >=1.72, Bun >=1.3, Node >=24, npm, curl, Python 3,
  staticcheck, gosec, govulncheck and grpcurl. Docker is required with --docker.

The script never prints provider keys or the process environment. The paid probe reads
DEEPSEEK_API_KEY or a regular file named by DEEPSEEK_API_KEY_FILE, scopes the value to
Go ADK and never passes it to OpenCode. Test credentials are ephemeral constants scoped
to local processes.
EOF
}

positive_integer() {
  [[ $1 =~ ^[1-9][0-9]*$ ]]
}

while (($# > 0)); do
  case "$1" in
    --bootstrap)
      bootstrap=1
      ;;
    --docker)
      docker_audit_enabled=1
      ;;
    --output)
      shift
      if (($# == 0)); then
        echo "--output requires a directory" >&2
        exit 2
      fi
      output_dir=$1
      ;;
    --requests)
      shift
      if (($# == 0)) || ! positive_integer "$1"; then
        echo "--requests requires a positive integer" >&2
        exit 2
      fi
      load_requests=$1
      ;;
    --concurrency)
      shift
      if (($# == 0)) || ! positive_integer "$1"; then
        echo "--concurrency requires a positive integer" >&2
        exit 2
      fi
      load_concurrency=$1
      ;;
    --require-platform)
      require_platform=1
      ;;
    --real-deepseek-acp)
      real_deepseek_acp=1
      ;;
    --deepseek-model)
      shift
      if (($# == 0)); then
        echo "--deepseek-model requires a model id" >&2
        exit 2
      fi
      deepseek_model=$1
      ;;
    --allow-missing-tools)
      strict_tools=0
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

if ((load_concurrency > load_requests)); then
  load_concurrency=$load_requests
fi
if ((require_platform == 1)) && [[ -z $platform_root ]]; then
  echo "the complete OMAI repository is required but packages/app was not found" >&2
  exit 2
fi
if [[ ! $deepseek_model =~ ^deepseek-[A-Za-z0-9._-]{1,240}$ ]]; then
  echo "--deepseek-model must be a DeepSeek model id without whitespace" >&2
  exit 2
fi
if ((real_deepseek_acp == 1)); then
  if [[ -z $opencode_test_command || -z $opencode_test_entry ]]; then
    echo "--real-deepseek-acp requires OMAI_TEST_OPENCODE_COMMAND and OMAI_TEST_OPENCODE_ENTRY" >&2
    exit 2
  fi
  if [[ -n $deepseek_api_key && -n $deepseek_api_key_file ]]; then
    echo "set only DEEPSEEK_API_KEY or DEEPSEEK_API_KEY_FILE, never both" >&2
    exit 2
  fi
  if [[ -n $deepseek_api_key_file ]]; then
    if [[ ! -f $deepseek_api_key_file || -L $deepseek_api_key_file || ! -r $deepseek_api_key_file ]]; then
      echo "DEEPSEEK_API_KEY_FILE must be a readable regular file, not a symlink" >&2
      exit 2
    fi
    deepseek_api_key=$(<"$deepseek_api_key_file")
  fi
  if [[ -z $deepseek_api_key ]]; then
    echo "--real-deepseek-acp requires DEEPSEEK_API_KEY or DEEPSEEK_API_KEY_FILE" >&2
    exit 2
  fi
  if [[ ! $deepseek_api_key =~ ^sk-[A-Za-z0-9_-]{20,512}$ ]]; then
    echo "DEEPSEEK_API_KEY has an invalid format" >&2
    exit 2
  fi
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
if [[ -z $output_dir ]]; then
  output_dir="$backend_root/audit-results/$stamp-$$"
elif [[ $output_dir != /* ]]; then
  output_dir="$PWD/$output_dir"
fi
if [[ -e $output_dir ]]; then
  echo "audit output already exists: $output_dir" >&2
  exit 2
fi
mkdir -p "$output_dir/logs" "$output_dir/bin" "$output_dir/live" "$output_dir/cache"
output_dir=$(cd -- "$output_dir" && pwd -P)

export GOCACHE="${GOCACHE:-$output_dir/cache/go-build}"
export GOMODCACHE="${GOMODCACHE:-$output_dir/cache/go-mod}"
export NPM_CONFIG_CACHE="$output_dir/cache/npm"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$output_dir/cache/xdg}"
if [[ " ${GOFLAGS:-} " != *" -buildvcs=false "* ]]; then
  export GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false"
fi
mkdir -p "$GOCACHE" "$GOMODCACHE" "$NPM_CONFIG_CACHE" "$XDG_CACHE_HOME"

declare -a result_names=()
declare -a result_statuses=()
declare -a result_seconds=()
declare -a result_logs=()
overall_status=0
step_number=0
started_epoch=$(date +%s)

append_result() {
  result_names+=("$1")
  result_statuses+=("$2")
  result_seconds+=("$3")
  result_logs+=("$4")
}

record_skip() {
  local name=$1
  local reason=$2
  step_number=$((step_number + 1))
  local log_rel="logs/$(printf '%02d' "$step_number").log"
  printf 'SKIP: %s\n' "$reason" | tee "$output_dir/$log_rel"
  append_result "$name" "SKIP" "0" "$log_rel"
}

run_step() {
  local name=$1
  shift
  step_number=$((step_number + 1))
  local log_rel="logs/$(printf '%02d' "$step_number").log"
  local step_started
  local step_finished
  local status
  local state
  step_started=$(date +%s)
  printf '\n[%02d] %s\n' "$step_number" "$name"
  (
    set -euo pipefail
    "$@"
  ) 2>&1 | tee "$output_dir/$log_rel"
  status=${PIPESTATUS[0]}
  step_finished=$(date +%s)
  if ((status == 0)); then
    state=PASS
  else
    state=FAIL
    overall_status=1
  fi
  append_result "$name" "$state" "$((step_finished - step_started))" "$log_rel"
  printf '[%s] %s (%ss)\n' "$state" "$name" "$((step_finished - step_started))"
  return 0
}

render_report() {
  local exit_status=$?
  if ((exit_status != 0)) && ((overall_status == 0)); then
    overall_status=$exit_status
  fi
  local finished_epoch
  local final_state=PASS
  local skipped=0
  local missing_tools=0
  local index
  finished_epoch=$(date +%s)
  if ((overall_status != 0)); then
    final_state=FAIL
  fi
  if [[ -s $output_dir/missing-tools.txt ]]; then
    missing_tools=$(wc -l < "$output_dir/missing-tools.txt")
    if [[ $final_state == PASS ]]; then
      final_state=PASS_WITH_SKIPS
    fi
  fi
  for index in "${!result_statuses[@]}"; do
    if [[ ${result_statuses[$index]} == SKIP ]]; then
      skipped=$((skipped + 1))
    fi
  done
  {
    printf '# OMAI Linux live audit\n\n'
    printf -- '- Result: **%s**\n' "$final_state"
    printf -- '- Started (UTC): `%s`\n' "$stamp"
    printf -- '- Duration: `%ss`\n' "$((finished_epoch - started_epoch))"
    printf -- '- Backend: `%s`\n' "$backend_root"
    if [[ -n $platform_root ]]; then
      printf -- '- Platform: `%s`\n' "$platform_root"
    else
      printf -- '- Platform: not present\n'
    fi
    printf -- '- Load probe: `%s` requests / `%s` workers\n' "$load_requests" "$load_concurrency"
    if ((real_deepseek_acp == 1)); then
      printf -- '- Paid provider probe: `OpenCode ACP -> Go capability edge -> Go ADK -> DeepSeek/%s`\n' "$deepseek_model"
    else
      printf -- '- Paid provider probe: disabled\n'
    fi
    if [[ -n $redis_test_addr ]]; then
      printf -- '- Redis-backed live state: enabled\n'
    else
      printf -- '- Redis-backed live state: disabled\n'
    fi
    printf -- '- Skipped steps: `%s`\n\n' "$skipped"
    if ((missing_tools > 0)); then
      printf -- '- Missing full-audit tools: `%s`\n\n' "$(paste -sd, "$output_dir/missing-tools.txt")"
    fi
    printf '## Results\n\n'
    printf '| Step | Status | Seconds | Log |\n'
    printf '|---|---:|---:|---|\n'
    for index in "${!result_names[@]}"; do
      printf '| %s | %s | %s | [%s](%s) |\n' \
        "${result_names[$index]}" "${result_statuses[$index]}" \
        "${result_seconds[$index]}" "${result_logs[$index]}" "${result_logs[$index]}"
    done
    printf '\n## Evidence\n\n'
    printf -- '- `environment.txt`: kernel and exact tool versions.\n'
    printf -- '- `source-files.sha256`: content manifest of the audited source tree.\n'
    printf -- '- `live/`: server log, ConnectRPC responses, reflection list and load metrics.\n'
    if ((real_deepseek_acp == 1)); then
      printf -- '- `live/deepseek-acp.json`: credential-safe evidence for the real ACP/ADK/provider turn.\n'
    fi
    printf -- '- `bin/`: statically built OMAI control-plane, executor, voice and ADK binaries.\n'
    printf -- '- `logs/`: unabridged output for every audit step.\n\n'
    printf 'Only PASS is a full release-acceptance result; PASS_WITH_SKIPS is diagnostic.\n'
    printf 'A PASS proves the checked source and local Linux execution path. It does not replace\n'
    printf 'a penetration test of the final OpenSandbox/Firecracker image, production certificates,\n'
    printf 'provider accounts, PostgreSQL/Redis durability or the target cluster network policy.\n'
  } > "$output_dir/REPORT.md"
  {
    printf 'step\tstatus\tseconds\tlog\n'
    for index in "${!result_names[@]}"; do
      printf '%s\t%s\t%s\t%s\n' \
        "${result_names[$index]}" "${result_statuses[$index]}" \
        "${result_seconds[$index]}" "${result_logs[$index]}"
    done
  } > "$output_dir/summary.tsv"
  printf '\nOMAI audit result: %s\nReport: %s/REPORT.md\n' "$final_state" "$output_dir"
}
trap render_report EXIT

version_at_least() {
  local actual=${1#v}
  local minimum=${2#v}
  [[ $(printf '%s\n%s\n' "$minimum" "$actual" | sort -V | head -n 1) == "$minimum" ]]
}

toolchain_preflight() {
  local required=(go buf node npm curl python3 rg sha256sum)
  local tool
  local go_version
  local buf_version
  local node_version
  for tool in "${required[@]}"; do
    command -v "$tool" >/dev/null || {
      echo "missing required tool: $tool"
      return 1
    }
  done
  if [[ -n $platform_root ]]; then
    command -v bun >/dev/null || {
      echo "missing required platform tool: bun"
      return 1
    }
  fi
  if [[ $(uname -s) != Linux ]]; then
    echo "this audit is intentionally Linux-only"
    return 1
  fi
  go_version=$(go env GOVERSION)
  buf_version=$(buf --version)
  node_version=$(node --version)
  version_at_least "${go_version#go}" 1.26 || {
    echo "Go >=1.26 is required; found $go_version"
    return 1
  }
  version_at_least "$buf_version" 1.72 || {
    echo "Buf >=1.72 is required; found $buf_version"
    return 1
  }
  version_at_least "$node_version" 24 || {
    echo "Node >=24 is required; found $node_version"
    return 1
  }
  if [[ -n $platform_root ]]; then
    version_at_least "$(bun --version)" 1.3 || {
      echo "Bun >=1.3 is required"
      return 1
    }
  fi
  local optional=(staticcheck gosec govulncheck grpcurl)
  if ((docker_audit_enabled == 1)); then
    optional+=(docker)
  fi
  for tool in "${optional[@]}"; do
    if ! command -v "$tool" >/dev/null; then
      if ((strict_tools == 1)); then
        echo "missing full-audit tool: $tool"
        return 1
      fi
      echo "optional tool unavailable: $tool"
      printf '%s\n' "$tool" >> "$output_dir/missing-tools.txt"
    fi
  done
  {
    uname -a
    go version
    printf 'buf %s\n' "$buf_version"
    node --version
    npm --version
    if command -v bun >/dev/null; then bun --version; fi
    if command -v staticcheck >/dev/null; then staticcheck -version; fi
    if command -v gosec >/dev/null; then gosec -version; fi
    if command -v govulncheck >/dev/null; then govulncheck -version; fi
    if command -v grpcurl >/dev/null; then grpcurl -version; fi
    if ((docker_audit_enabled == 1)) && command -v docker >/dev/null; then
      docker version --format '{{.Client.Version}}'
    fi
  } | tee "$output_dir/environment.txt"
}

bootstrap_dependencies() {
  command -v go >/dev/null || return 1
  command -v npm >/dev/null || return 1
  (
    cd "$backend_root" || exit 1
    go mod download
    cd integration/adk || exit 1
    go mod download
  )
  (
    cd "$backend_root/sdk/typescript" || exit 1
    npm ci --no-audit --no-fund
  )
  if [[ -n $platform_root ]]; then
    command -v bun >/dev/null || return 1
    (
      cd "$platform_root" || exit 1
      bun install --frozen-lockfile
    )
  fi
}

source_manifest() {
  local manifest_root=$backend_root
  if [[ -n $platform_root ]]; then
    manifest_root=$platform_root
  fi
  (
    cd "$manifest_root" || exit 1
    find . \
      \( -type d \( -name .git -o -name node_modules -o -name .cache -o -name .turbo -o -name audit-results -o -name coverage -o -name dist -o -name build -o -name test-results -o -name playwright-report \) -prune \) -o \
      \( -type f ! -name '*.zip' ! -name '*.tgz' -print0 \) |
      sort -z |
      xargs -0 -r sha256sum
  ) > "$output_dir/source-files.sha256"
  test -s "$output_dir/source-files.sha256"
  wc -l "$output_dir/source-files.sha256"
}

portal_migration_boundary() {
  if [[ -z $platform_root ]]; then
    echo "complete Portal source is not present"
    return 1
  fi
  local source="$platform_root/packages/app/src"
  local report="$output_dir/portal-legacy-runtime-calls.txt"
  local files="$output_dir/portal-legacy-runtime-files.txt"
  local unexpected="$output_dir/portal-unexpected-runtime-files.txt"
  local allowed="$output_dir/portal-allowed-compatibility-files.txt"
  local workspace_pattern='\.client\.(file|find|path|pty|vcs|lsp|mcp|permission|question|project)|sdk\.mcp|experimental\.resource|/global/file/create|/file/(download|rename|remove)|terminal-websocket-url'
  local runtime_pattern='\.client\.(provider|session|global)|serverSDK\(\)\.client\.global'

  if rg -n "$workspace_pattern" "$source" --glob '!**/*.test.*' --glob '!**/*.spec.*'; then
    echo "a migrated workspace domain still calls the OpenCode runtime"
    return 1
  fi

  rg -n "$runtime_pattern" "$source" --glob '!**/*.test.*' --glob '!**/*.spec.*' > "$report" || true
  rg -l "$runtime_pattern" "$source" --glob '!**/*.test.*' --glob '!**/*.spec.*' |
    sed "s#^$platform_root/##" | sort -u > "$files" || true
  cat > "$allowed" <<'EOF'
packages/app/src/components/dialog-connect-provider.tsx
packages/app/src/components/dialog-fork.tsx
packages/app/src/components/prompt-input/submit.ts
packages/app/src/components/settings-providers.tsx
packages/app/src/components/settings-v2/providers.tsx
packages/app/src/context/server-sync.tsx
packages/app/src/pages/session/use-session-commands.tsx
EOF
  comm -23 "$files" "$allowed" > "$unexpected"
  if [[ -s $unexpected ]]; then
    echo "new OpenCode runtime call sites escaped the audited compatibility queue:"
    cat "$unexpected"
    return 1
  fi

  if find "$platform_root" \
    \( -type d \( -name .git -o -name node_modules \) -prune \) -o \
    \( -type f -name '*.proto' ! -path "$backend_root/api/*" -print -quit \) | grep -q .; then
    echo "a non-canonical Proto copy exists outside services/omai-control-plane/api"
    return 1
  fi
  printf 'workspace OpenCode runtime calls: 0\n'
  printf 'remaining audited compatibility call sites: %s across %s files\n' \
    "$(wc -l < "$report")" "$(wc -l < "$files")"
  printf 'Proto copies outside canonical owner: 0\n'
}

gofmt_check() {
  local output
  output=$(
    find "$backend_root" -type f -name '*.go' -not -path '*/gen/*' -print0 |
      xargs -0 -r gofmt -l
  )
  if [[ -n $output ]]; then
    printf 'unformatted Go files:\n%s\n' "$output"
    return 1
  fi
  echo "all non-generated Go sources are formatted"
}

tree_digest() {
  local directory
  for directory in "$@"; do
    if [[ -d $directory ]]; then
      find "$directory" -type f -print0
    fi
  done |
    sort -z |
    xargs -0 -r sha256sum |
    sha256sum |
    awk '{print $1}'
}

buf_contract_check() {
  local before
  local after
  cd "$backend_root" || return 1
  before=$(tree_digest gen/go sdk/typescript/src/gen)
  buf lint
  buf generate
  after=$(tree_digest gen/go sdk/typescript/src/gen)
  if [[ $before != "$after" ]]; then
    echo "generated Protobuf output drifted; inspect the regenerated files"
    return 1
  fi
  echo "Buf lint passed and committed generated output is current"
}

root_module_verify() {
  cd "$backend_root" || return 1
  go mod verify
  go vet ./...
}

root_race_tests() {
  cd "$backend_root" || return 1
  go test -race -count=1 ./...
}

redis_store_e2e() {
  cd "$backend_root" || return 1
  OMAI_REDIS_TEST_ADDR="$redis_test_addr" \
    go test ./internal/adapter/redisstore -run '^TestPlatformStoresAgainstRedis$' -count=1 -v
}

root_build() {
  cd "$backend_root" || return 1
  CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$output_dir/bin/omai" ./cmd/omai-server
  CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$output_dir/bin/omai-workspace-executor" ./cmd/omai-workspace-executor
  CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$output_dir/bin/omai-voice-gateway" ./cmd/omai-voice-gateway
  CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$output_dir/bin/omai-opencode-acp-probe" ./cmd/omai-opencode-acp-probe
}

root_staticcheck() {
  cd "$backend_root" || return 1
  staticcheck ./...
}

root_gosec() {
  cd "$backend_root" || return 1
  gosec -quiet -exclude-generated -nosec-require-justification -nosec-require-rules ./...
}

root_govulncheck() {
  cd "$backend_root" || return 1
  govulncheck ./...
}

adk_verify() {
  cd "$backend_root/integration/adk" || return 1
  go mod verify
  go vet ./...
  go test -race -count=1 ./...
  CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$output_dir/bin/omai-adk-runtime" ./cmd/omai-adk-runtime
}

adk_staticcheck() {
  cd "$backend_root/integration/adk" || return 1
  staticcheck ./...
}

adk_gosec() {
  cd "$backend_root/integration/adk" || return 1
  gosec -quiet -exclude-generated -nosec-require-justification -nosec-require-rules ./...
}

adk_govulncheck() {
  cd "$backend_root/integration/adk" || return 1
  govulncheck ./...
}

sdk_verify() {
  cd "$backend_root/sdk/typescript" || return 1
  if [[ ! -x node_modules/.bin/tsc ]] || [[ ! -x node_modules/.bin/vitest ]]; then
    echo "SDK dependencies are absent; rerun with --bootstrap"
    return 1
  fi
  node_modules/.bin/tsc -p tsconfig.json --noEmit
  node_modules/.bin/vitest run
  node_modules/.bin/tsc -p tsconfig.build.json
  node --input-type=module --eval "await import('./dist/index.js'); await import('./dist/proto.js')"
  python3 - <<'PY'
import json
from pathlib import Path

root = Path.cwd()
package = json.loads((root / "package.json").read_text(encoding="utf-8"))
assert package.get("files") == ["dist", "README.md"], package.get("files")
assert package.get("sideEffects") is False
for descriptor in package.get("exports", {}).values():
    for target in descriptor.values():
        path = root / target.removeprefix("./")
        assert path.is_file(), f"missing package export: {target}"
files = [path for path in (root / "dist").rglob("*") if path.is_file()]
assert files, "dist is empty"
assert all(path.suffix in {".js", ".ts", ".map"} for path in files), files
print(f"validated {len(files)} SDK distribution files and every declared export")
PY
}

portal_typecheck() {
  cd "$platform_root" || return 1
  if [[ ! -d node_modules ]]; then
    echo "Portal dependencies are absent; rerun with --bootstrap"
    return 1
  fi
  bun run typecheck
}

portal_unit_tests() {
  cd "$platform_root" || return 1
  if [[ ! -d node_modules ]]; then
    echo "Portal dependencies are absent; rerun with --bootstrap"
    return 1
  fi
  CI=1 bun run test
}

portal_production_build() {
  cd "$platform_root" || return 1
  if [[ ! -d node_modules ]]; then
    echo "Portal dependencies are absent; rerun with --bootstrap"
    return 1
  fi
  bun run build
}

opencode_source_e2e() {
  cd "$backend_root" || return 1
  OMAI_TEST_OPENCODE_COMMAND="$opencode_test_command" \
    OMAI_TEST_OPENCODE_ENTRY="$opencode_test_entry" \
    go test ./internal/adapter/harness -run '^TestOpenCodeSourceEndToEnd$' -count=1 -v
}

real_deepseek_acp_e2e() {
  local adk_port
  local adk_addr
  local adk_url
  local runtime_token
  local provider_config="$output_dir/live/deepseek-provider.json"
  local workspace="$output_dir/live/deepseek-workspace"
  local opencode_home="$output_dir/live/deepseek-opencode-home"
  local adk_log="$output_dir/live/deepseek-adk.log"
  local health_file="$output_dir/live/deepseek-health.json"
  local result_file="$output_dir/live/deepseek-acp.json"
  local adk_pid
  local status=""
  local attempt

  adk_port=$(pick_port) || return 1
  adk_addr="127.0.0.1:$adk_port"
  adk_url="http://$adk_addr"
  runtime_token=$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(48))
PY
  ) || return 1
  mkdir -p "$workspace" "$opencode_home"
  chmod 700 "$workspace" "$opencode_home"
  python3 - "$provider_config" "$deepseek_model" <<'PY' || return 1
import json
import sys

path, model = sys.argv[1:]
document = {
    "schema_version": "1",
    "max_cached_models": 4,
    "default": {"provider_id": "deepseek", "model_id": model},
    "providers": [{
        "id": "deepseek",
        "name": "DeepSeek",
        "driver": "openai-chat-completions",
        "api_key_env": "DEEPSEEK_API_KEY",
        "base_url": "https://api.deepseek.com",
        "default_model": model,
        "model_prefixes": ["deepseek-"],
        "request_timeout": "3m",
        "enabled": True,
    }],
}
with open(path, "w", encoding="utf-8") as target:
    json.dump(document, target, indent=2)
    target.write("\n")
PY
  chmod 600 "$provider_config"

  (
    cd "$backend_root/integration/adk" || exit 1
    DEEPSEEK_API_KEY="$deepseek_api_key" \
      OMAI_RUNTIME_TOKEN="$runtime_token" \
      OMAI_ADK_ENV=development \
      OMAI_ADK_ADDR="$adk_addr" \
      OMAI_ADK_PROVIDERS_FILE="$provider_config" \
      "$output_dir/bin/omai-adk-runtime"
  ) > "$adk_log" 2>&1 &
  adk_pid=$!
  trap "kill -TERM $adk_pid 2>/dev/null || true; wait $adk_pid 2>/dev/null || true" EXIT

  for attempt in $(seq 1 150); do
    status=$(curl --silent --show-error --output "$health_file" --write-out '%{http_code}' \
      --header 'Content-Type: application/json' \
      --header 'Connect-Protocol-Version: 1' \
      --header "Authorization: Bearer $runtime_token" \
      --data '{}' \
      "$adk_url/uab.v1.ModelGatewayService/Health" || true)
    if [[ $status == 200 ]]; then
      break
    fi
    if ! kill -0 "$adk_pid" 2>/dev/null; then
      echo "Go ADK runtime stopped before its health endpoint became ready"
      return 1
    fi
    sleep 0.1
  done
  if [[ $status != 200 ]]; then
    echo "Go ADK runtime did not become ready"
    return 1
  fi
  python3 - "$health_file" <<'PY' || return 1
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    health = json.load(source)
assert health.get("available") is True, health
assert health.get("authenticated") is True, health
PY

  OMAI_TEST_OPENCODE_COMMAND="$opencode_test_command" \
    OMAI_TEST_OPENCODE_ENTRY="$opencode_test_entry" \
    OMAI_TEST_WORKSPACE="$workspace" \
    OMAI_TEST_OPENCODE_HOME="$opencode_home" \
    OMAI_TEST_MODEL_GATEWAY_URL="$adk_url" \
    OMAI_TEST_MODEL_GATEWAY_TOKEN="$runtime_token" \
    OMAI_TEST_PROVIDER_ID=deepseek \
    OMAI_TEST_MODEL_ID="$deepseek_model" \
    OMAI_TEST_TIMEOUT=3m \
    "$output_dir/bin/omai-opencode-acp-probe" | tee "$result_file" || return 1

  python3 - "$result_file" <<'PY' || return 1
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result.get("acp_transport") == "stdio-jsonrpc", result
assert result.get("agent") == "OpenCode", result
assert result.get("opencode_provider") == "omai", result
assert result.get("go_provider_route") == "deepseek", result
assert result.get("stop_reason") == "end_turn", result
assert result.get("sentinel_observed") is True, result
assert result.get("go_owns_provider_credential") is True, result
assert result.get("credential_in_harness") is False, result
PY

  kill -TERM "$adk_pid"
  wait "$adk_pid" || return 1
  trap - EXIT

  printf '%s' "$deepseek_api_key" | python3 -c '
import pathlib
import sys

secret = sys.stdin.buffer.read()
roots = [pathlib.Path(item) for item in sys.argv[1:]]
matches = []
for root in roots:
    for path in root.rglob("*"):
        if not path.is_file() or path.is_symlink():
            continue
        try:
            if secret and secret in path.read_bytes():
                matches.append(str(path))
        except OSError:
            pass
if matches:
    print("provider credential was written to audit evidence:")
    print("\n".join(matches))
    raise SystemExit(1)
print("verified: provider credential is absent from ACP, ADK and audit evidence")
' "$output_dir/logs" "$output_dir/live" || return 1
}

pick_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
    listener.bind(("127.0.0.1", 0))
    print(listener.getsockname()[1])
PY
}

connect_post() {
  local base_url=$1
  local token=$2
  local procedure=$3
  local body=$4
  local destination=$5
  local status
  status=$(curl --silent --show-error \
    --output "$destination" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header 'Connect-Protocol-Version: 1' \
    --header "Authorization: Bearer $token" \
    --data-binary "$body" \
    "$base_url/$procedure") || return 1
  if [[ $status != 200 ]]; then
    echo "ConnectRPC $procedure returned HTTP $status"
    cat "$destination"
    return 1
  fi
}

live_load_probe() {
  local url=$1
  local token=$2
  python3 - "$url" "$token" "$load_requests" "$load_concurrency" <<'PY'
import concurrent.futures
import json
import statistics
import sys
import time
import urllib.request

url, token, request_count, concurrency = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
body = b"{}"

def call(_):
    request = urllib.request.Request(
        url,
        data=body,
        method="POST",
        headers={
            "Authorization": f"Bearer {token}",
            "Connect-Protocol-Version": "1",
            "Content-Type": "application/json",
        },
    )
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            payload = json.load(response)
            ok = response.status == 200 and payload.get("ok") is True
            return ok, (time.perf_counter() - started) * 1000, response.status, ""
    except Exception as error:
        return False, (time.perf_counter() - started) * 1000, 0, str(error)

started = time.perf_counter()
with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
    results = list(pool.map(call, range(request_count)))
elapsed = time.perf_counter() - started
latencies = sorted(item[1] for item in results)
failures = [item for item in results if not item[0]]

def percentile(value):
    index = min(len(latencies) - 1, max(0, round((len(latencies) - 1) * value)))
    return round(latencies[index], 3)

report = {
    "requests": request_count,
    "concurrency": concurrency,
    "passed": request_count - len(failures),
    "failed": len(failures),
    "elapsed_seconds": round(elapsed, 3),
    "requests_per_second": round(request_count / elapsed, 2),
    "latency_ms": {
        "min": round(latencies[0], 3),
        "mean": round(statistics.fmean(latencies), 3),
        "p50": percentile(0.50),
        "p95": percentile(0.95),
        "p99": percentile(0.99),
        "max": round(latencies[-1], 3),
    },
    "first_errors": [item[3] or f"HTTP {item[2]}" for item in failures[:5]],
}
print(json.dumps(report, indent=2))
if failures:
    raise SystemExit(1)
PY
}

live_server_smoke() {
  local api_port
  local metrics_port
  local api_addr
  local base_url
  local token='omai-live-audit-development-token-000001'
  local workspace="$output_dir/live/workspace"
  local server_pid
  local status
  local attempt
  local session_id
  local workspace_id
  local initial_revision
  local terminal_id
  local preview_url
  local restarted_preview_url
  local preview_workspace_id
  api_port=$(pick_port) || return 1
  metrics_port=$(pick_port) || return 1
  api_addr="127.0.0.1:$api_port"
  base_url="http://$api_addr"
  mkdir -p "$workspace"
	python3 - "$workspace/index.html" <<'PY' || return 1
import pathlib
import sys

pathlib.Path(sys.argv[1]).write_text("<!doctype html><h1>OMAI Go preview generation one</h1>\n", encoding="utf-8")
PY
  (
    cd "$backend_root" || exit 1
    if [[ -n $redis_test_addr ]]; then
      export OMAI_REDIS_ADDR="$redis_test_addr"
    fi
    OMAI_ENV=development \
      OMAI_ADDR="$api_addr" \
      OMAI_METRICS_ADDR="127.0.0.1:$metrics_port" \
      OMAI_WORKSPACE_ROOTS="$workspace" \
      OMAI_DEV_TOKEN="$token" \
      OMAI_ALLOWED_ORIGINS=http://127.0.0.1:4444 \
		OMAI_PREVIEW_PUBLIC_BASE_URL="$base_url" \
		OMAI_PREVIEW_PREPARATION=never \
      OMAI_ENABLE_REFLECTION=true \
      OMAI_ENABLE_DEMO_RUNTIME=true \
      OMAI_MODEL_CATALOG_FILE=./configs/models.example.json \
      OMAI_RATE_PER_SECOND=100000 \
      OMAI_RATE_BURST=100000 \
      "$output_dir/bin/omai"
  ) > "$output_dir/live/server.log" 2>&1 &
  server_pid=$!
  trap "kill -TERM $server_pid 2>/dev/null || true; wait $server_pid 2>/dev/null || true" EXIT

  for attempt in $(seq 1 100); do
    status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
      --header "Authorization: Bearer $token" "$base_url/livez" || true)
    if [[ $status == 204 ]]; then
      break
    fi
    sleep 0.1
  done
  if [[ $status != 204 ]]; then
    echo "control plane did not become live"
    cat "$output_dir/live/server.log"
    return 1
  fi

  connect_post "$base_url" "$token" 'uab.v1.ControlPlaneService/Health' '{}' "$output_dir/live/health.json" || return 1
  connect_post "$base_url" "$token" 'uab.v1.ControlPlaneService/ListRuntimes' '{}' "$output_dir/live/runtimes.json" || return 1
  connect_post "$base_url" "$token" 'uab.v1.ModelCatalogService/ListModels' '{}' "$output_dir/live/models.json" || return 1
  python3 - "$output_dir/live/health.json" "$output_dir/live/runtimes.json" <<'PY' || return 1
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    health = json.load(source)
with open(sys.argv[2], encoding="utf-8") as source:
    runtimes = json.load(source)
assert health.get("ok") is True, health
assert any(item.get("id") == "go-adk-demo" for item in runtimes.get("runtimes", [])), runtimes
PY

  python3 - "$workspace" > "$output_dir/live/workspace-resolve-request.json" <<'PY'
import json
import sys

json.dump({"root": sys.argv[1]}, sys.stdout)
PY
  connect_post "$base_url" "$token" 'uab.v1.WorkspaceService/ResolveWorkspace' \
    "@$output_dir/live/workspace-resolve-request.json" "$output_dir/live/workspace-resolve.json" || return 1
  workspace_id=$(python3 - "$output_dir/live/workspace-resolve.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
workspace = value["workspace"]
assert workspace["root"], workspace
print(workspace["id"])
PY
  ) || return 1

  connect_post "$base_url" "$token" 'uab.v1.WorkspaceService/CreateDirectory' \
    "{\"workspaceId\":\"$workspace_id\",\"path\":\"monaco-source\"}" \
    "$output_dir/live/workspace-create-directory.json" || return 1
  python3 - "$workspace_id" > "$output_dir/live/workspace-write-initial-request.json" <<'PY'
import base64
import json
import sys

json.dump({
    "workspaceId": sys.argv[1],
    "path": "monaco-source/main.go",
    "data": base64.b64encode(b"package main\n\nconst generation = 1\n").decode("ascii"),
}, sys.stdout)
PY
  connect_post "$base_url" "$token" 'uab.v1.WorkspaceService/WriteFile' \
    "@$output_dir/live/workspace-write-initial-request.json" "$output_dir/live/workspace-write-initial.json" || return 1
  initial_revision=$(python3 - "$output_dir/live/workspace-write-initial.json" <<'PY'
import json
import re
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
revision = value["revision"]
assert re.fullmatch(r"sha256:[0-9a-f]{64}", revision), value
print(revision)
PY
  ) || return 1
  python3 - "$workspace_id" "$initial_revision" > "$output_dir/live/workspace-write-cas-request.json" <<'PY'
import base64
import json
import sys

json.dump({
    "workspaceId": sys.argv[1],
    "path": "monaco-source/main.go",
    "data": base64.b64encode(b"package main\n\nconst generation = 2\n").decode("ascii"),
    "expectedRevision": sys.argv[2],
    "requireRevisionMatch": True,
}, sys.stdout)
PY
  connect_post "$base_url" "$token" 'uab.v1.WorkspaceService/WriteFile' \
    "@$output_dir/live/workspace-write-cas-request.json" "$output_dir/live/workspace-write-cas.json" || return 1
  status=$(curl --silent --show-error --output "$output_dir/live/workspace-stale-write.json" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header 'Connect-Protocol-Version: 1' \
    --header "Authorization: Bearer $token" \
    --data-binary "@$output_dir/live/workspace-write-cas-request.json" \
    "$base_url/uab.v1.WorkspaceService/WriteFile") || return 1
  if [[ $status == 200 ]]; then
    echo "stale Monaco write unexpectedly succeeded"
    return 1
  fi
  python3 - "$output_dir/live/workspace-stale-write.json" <<'PY' || return 1
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert value.get("code") == "aborted", value
PY
  connect_post "$base_url" "$token" 'uab.v1.WorkspaceService/MovePath' \
    "{\"workspaceId\":\"$workspace_id\",\"sourcePath\":\"monaco-source\",\"destinationPath\":\"app\"}" \
    "$output_dir/live/workspace-move-directory.json" || return 1
  connect_post "$base_url" "$token" 'uab.v1.WorkspaceService/ReadFile' \
    "{\"workspaceId\":\"$workspace_id\",\"path\":\"app/main.go\"}" \
    "$output_dir/live/workspace-read-after-move.json" || return 1
  python3 - "$output_dir/live/workspace-read-after-move.json" <<'PY' || return 1
import base64
import json
import re
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert base64.b64decode(value["data"]) == b"package main\n\nconst generation = 2\n", value
assert re.fullmatch(r"sha256:[0-9a-f]{64}", value["revision"]), value
PY
  connect_post "$base_url" "$token" 'uab.v1.WorkspaceService/DeletePath' \
    "{\"workspaceId\":\"$workspace_id\",\"path\":\"app\",\"recursive\":true}" \
    "$output_dir/live/workspace-delete-directory.json" || return 1

  connect_post "$base_url" "$token" 'uab.v1.GitService/Init' \
    "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/live/git-init.json" || return 1
  connect_post "$base_url" "$token" 'uab.v1.GitService/Status' \
    "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/live/git-status.json" || return 1
  python3 - "$output_dir/live/git-status.json" <<'PY' || return 1
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert "status" in value, value
assert any(item.get("path") == "index.html" for item in value["status"].get("files", [])), value
PY
  connect_post "$base_url" "$token" 'uab.v1.LSPService/List' \
    "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/live/lsp-list.json" || return 1
  python3 - "$output_dir/live/lsp-list.json" <<'PY' || return 1
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert value.get("servers"), value
PY
  connect_post "$base_url" "$token" 'uab.v1.TerminalService/Create' \
    "{\"workspaceId\":\"$workspace_id\",\"command\":\"/bin/sh\",\"args\":[\"-lc\",\"printf OMAI_TERMINAL_OK\\\\n\"]}" \
    "$output_dir/live/terminal-create.json" || return 1
  terminal_id=$(python3 - "$output_dir/live/terminal-create.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
print(value["terminal"]["id"])
PY
  ) || return 1
  for attempt in $(seq 1 50); do
    connect_post "$base_url" "$token" 'uab.v1.TerminalService/List' \
      "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/live/terminal-list.json" || return 1
    if python3 - "$output_dir/live/terminal-list.json" "$terminal_id" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    values = json.load(source).get("terminals", [])
terminal = next((item for item in values if item.get("id") == sys.argv[2]), None)
raise SystemExit(0 if terminal and terminal.get("status") == "exited" and terminal.get("exitCode", 0) == 0 else 1)
PY
    then
      break
    fi
    sleep 0.1
  done
  if ((attempt == 50)); then
    echo "Go-owned terminal did not exit successfully"
    return 1
  fi
  connect_post "$base_url" "$token" 'uab.v1.TerminalService/Remove' \
    "{\"terminalId\":\"$terminal_id\"}" "$output_dir/live/terminal-remove.json" || return 1

  python3 - "$workspace" > "$output_dir/live/preview-start-request.json" <<'PY'
import json
import sys

json.dump({"root": sys.argv[1]}, sys.stdout)
PY
  connect_post "$base_url" "$token" 'uab.v1.PreviewService/Start' \
    "@$output_dir/live/preview-start-request.json" "$output_dir/live/preview-start.json" || return 1
  readarray -t preview_fields < <(python3 - "$output_dir/live/preview-start.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    preview = json.load(source)["preview"]
assert preview["status"] == "ready", preview
assert preview["framework"] == "static", preview
assert preview["port"] > 0, preview
assert preview["publicUrl"].startswith("http://127.0.0.1:"), preview
print(preview["publicUrl"])
print(preview["workspaceId"])
PY
  ) || return 1
  preview_url=${preview_fields[0]}
  preview_workspace_id=${preview_fields[1]}
  curl --fail --silent --show-error "$preview_url" --output "$output_dir/live/preview-first.html" || return 1
  grep -q 'OMAI Go preview generation one' "$output_dir/live/preview-first.html" || return 1

	python3 - "$workspace/index.html" <<'PY' || return 1
import pathlib
import sys

pathlib.Path(sys.argv[1]).write_text("<!doctype html><h1>OMAI Go preview generation two</h1>\n", encoding="utf-8")
PY
  connect_post "$base_url" "$token" 'uab.v1.PreviewService/Restart' \
    "@$output_dir/live/preview-start-request.json" "$output_dir/live/preview-restart.json" || return 1
  restarted_preview_url=$(python3 - "$output_dir/live/preview-start.json" "$output_dir/live/preview-restart.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    before = json.load(source)["preview"]
with open(sys.argv[2], encoding="utf-8") as source:
    after = json.load(source)["preview"]
assert after["status"] == "ready", after
assert before["id"] != after["id"], (before, after)
assert before["processId"] != after["processId"], (before, after)
assert before["publicUrl"] != after["publicUrl"], (before, after)
print(after["publicUrl"])
PY
  ) || return 1
  curl --fail --silent --show-error "$restarted_preview_url" --output "$output_dir/live/preview-second.html" || return 1
  grep -q 'OMAI Go preview generation two' "$output_dir/live/preview-second.html" || return 1
  status=$(curl --silent --output /dev/null --write-out '%{http_code}' "$preview_url" || true)
  if [[ $status != 404 ]]; then
    echo "replaced preview capability returned HTTP $status, expected 404"
    return 1
  fi
  connect_post "$base_url" "$token" 'uab.v1.PreviewService/Stop' \
    "{\"workspaceId\":\"$preview_workspace_id\"}" "$output_dir/live/preview-stop.json" || return 1
  status=$(curl --silent --output /dev/null --write-out '%{http_code}' "$restarted_preview_url" || true)
  if [[ $status != 404 ]]; then
    echo "stopped preview capability returned HTTP $status, expected 404"
    return 1
  fi

  python3 - "$workspace" > "$output_dir/live/prompt-request.json" <<'PY'
import json
import sys

json.dump({
    "runtimeId": "go-adk-demo",
    "externalSessionId": "omai-linux-live-audit",
    "root": sys.argv[1],
    "text": "Return the deterministic OMAI live-audit response.",
    "title": "Linux live audit",
}, sys.stdout)
PY
  connect_post "$base_url" "$token" 'uab.v1.ControlPlaneService/Prompt' \
    "@$output_dir/live/prompt-request.json" "$output_dir/live/prompt.json" || return 1
  session_id=$(python3 - "$output_dir/live/prompt.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert value.get("accepted") is True, value
print(value["sessionId"])
PY
  ) || return 1
  for attempt in $(seq 1 50); do
    connect_post "$base_url" "$token" 'uab.v1.ConversationService/ListMessages' \
      "{\"sessionId\":\"$session_id\"}" "$output_dir/live/messages.json" || return 1
    if python3 - "$output_dir/live/messages.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
text = "".join(item.get("text", "") for item in value.get("messages", []) if item.get("role") == "assistant")
raise SystemExit(0 if "OMAI demo runtime received" in text else 1)
PY
    then
      break
    fi
    sleep 0.1
  done
  if ((attempt == 50)); then
    echo "demo runtime response was not persisted"
    cat "$output_dir/live/messages.json"
    return 1
  fi

  status=$(curl --silent --output "$output_dir/live/unauthenticated.json" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' --data '{}' \
    "$base_url/uab.v1.ControlPlaneService/ListRuntimes" || true)
  if [[ $status != 401 ]]; then
    echo "unauthenticated ConnectRPC request returned HTTP $status, expected 401"
    return 1
  fi
  status=$(curl --silent --output "$output_dir/live/outside-workspace.json" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header 'Connect-Protocol-Version: 1' \
    --header "Authorization: Bearer $token" \
    --data '{"root":"/etc","name":"forbidden"}' \
    "$base_url/omai.platform.v1.ProjectService/ResolveProject" || true)
  if [[ $status == 200 ]]; then
    echo "workspace containment probe unexpectedly accepted /etc"
    return 1
  fi
  curl --silent --show-error --dump-header "$output_dir/live/untrusted-origin.headers" \
    --output /dev/null --header "Authorization: Bearer $token" \
    --header 'Origin: https://untrusted.invalid' "$base_url/livez" || return 1
  if grep -qi '^access-control-allow-origin:' "$output_dir/live/untrusted-origin.headers"; then
    echo "untrusted origin received an Access-Control-Allow-Origin header"
    return 1
  fi

  if command -v grpcurl >/dev/null; then
    grpcurl -plaintext -H "Authorization: Bearer $token" "$api_addr" list |
      tee "$output_dir/live/reflection.txt"
    grep -qx 'uab.v1.ControlPlaneService' "$output_dir/live/reflection.txt" || return 1
		grep -qx 'uab.v1.PreviewService' "$output_dir/live/reflection.txt" || return 1
    grep -qx 'omai.platform.v1.SessionService' "$output_dir/live/reflection.txt" || return 1
  elif ((strict_tools == 1)); then
    echo "grpcurl is required for the live native-gRPC reflection probe"
    return 1
  else
    echo "grpcurl unavailable; live native-gRPC reflection probe skipped"
  fi

  live_load_probe "$base_url/uab.v1.ControlPlaneService/Health" "$token" |
    tee "$output_dir/live/load.json" || return 1
  curl --fail --silent --show-error "http://127.0.0.1:$metrics_port/metrics" \
    --output "$output_dir/live/metrics.txt" || return 1

  kill -TERM "$server_pid"
  wait "$server_pid" || return 1
  trap - EXIT
  echo "live ConnectRPC, revision-safe workspace, Git, terminal, LSP, preview, policy and load probes completed"
}

docker_audit() {
  local tag_suffix
  local redis_port
  local redis_container
  local redis_ready=0
  local redis_test_status
  tag_suffix=$(printf '%s' "$stamp" | tr '[:upper:]' '[:lower:]')
  mkdir -p "$output_dir/docker-workspaces"
  cd "$backend_root" || return 1
  OMAI_DEV_TOKEN=omai-audit-development-token-000001 \
    OMAI_SERVICE_TOKEN=omai-audit-service-token-00000001 \
    OMAI_ADK_RUNTIME_TOKEN=omai-audit-adk-runtime-token-0001 \
    OMAI_EXECUTOR_TOKEN=omai-audit-executor-token-0000001 \
    OMAI_WORKSPACES_DIR="$output_dir/docker-workspaces" \
    GOOGLE_API_KEY=not-a-real-provider-key \
    docker compose config --quiet
  redis_port=$(pick_port) || return 1
  redis_container="omai-redis-audit-$tag_suffix"
  docker run --detach --rm --name "$redis_container" \
    --publish "127.0.0.1:$redis_port:6379" \
    redis:8.2.8-alpine redis-server --save '' --appendonly no --maxmemory-policy noeviction >/dev/null || return 1
  for _ in $(seq 1 50); do
    if docker exec "$redis_container" redis-cli ping 2>/dev/null | grep -q PONG; then
      redis_ready=1
      break
    fi
    sleep 0.1
  done
  if ((redis_ready == 0)); then
    docker logs "$redis_container" || true
    docker stop "$redis_container" >/dev/null 2>&1 || true
    return 1
  fi
  OMAI_REDIS_TEST_ADDR="127.0.0.1:$redis_port" \
    go test ./internal/adapter/redisstore -run '^TestPlatformStoresAgainstRedis$' -count=1 -v
  redis_test_status=$?
  docker stop "$redis_container" >/dev/null 2>&1 || true
  if ((redis_test_status != 0)); then
    return "$redis_test_status"
  fi
  docker build --target server -t "omai-audit-server:$tag_suffix" .
  docker build --target executor -t "omai-audit-executor:$tag_suffix" .
  docker build --target voice -t "omai-audit-voice:$tag_suffix" .
  docker build --target adk -t "omai-audit-adk:$tag_suffix" .
}

if ((bootstrap == 1)); then
  run_step "Locked dependency bootstrap" bootstrap_dependencies
fi
run_step "Linux toolchain preflight" toolchain_preflight
run_step "Audited source SHA-256 manifest" source_manifest
if [[ -n $platform_root ]]; then
  run_step "Portal Go-ownership source boundary" portal_migration_boundary
else
  record_skip "Portal Go-ownership source boundary" "complete platform repository is not present"
fi
run_step "Go formatting cleanliness" gofmt_check
run_step "Buf lint, generation and drift" buf_contract_check
run_step "Control-plane module verify and vet" root_module_verify
run_step "Control-plane race tests" root_race_tests
if [[ -n $redis_test_addr ]]; then
  run_step "Redis-backed platform repository E2E" redis_store_e2e
fi
run_step "Static Linux binary builds" root_build

if command -v staticcheck >/dev/null; then
  run_step "Control-plane staticcheck" root_staticcheck
else
  record_skip "Control-plane staticcheck" "staticcheck is unavailable"
fi
if command -v gosec >/dev/null; then
  run_step "Control-plane gosec" root_gosec
else
  record_skip "Control-plane gosec" "gosec is unavailable"
fi
if command -v govulncheck >/dev/null; then
  run_step "Control-plane govulncheck" root_govulncheck
else
  record_skip "Control-plane govulncheck" "govulncheck is unavailable"
fi

run_step "Go ADK module, race tests and build" adk_verify
if command -v staticcheck >/dev/null; then
  run_step "Go ADK staticcheck" adk_staticcheck
else
  record_skip "Go ADK staticcheck" "staticcheck is unavailable"
fi
if command -v gosec >/dev/null; then
  run_step "Go ADK gosec" adk_gosec
else
  record_skip "Go ADK gosec" "gosec is unavailable"
fi
if command -v govulncheck >/dev/null; then
  run_step "Go ADK govulncheck" adk_govulncheck
else
  record_skip "Go ADK govulncheck" "govulncheck is unavailable"
fi

run_step "TypeScript SDK strict verification" sdk_verify
if [[ -n $platform_root ]]; then
  run_step "SolidJS workspace typecheck" portal_typecheck
  run_step "SolidJS workspace unit tests" portal_unit_tests
  run_step "SolidJS Portal production build" portal_production_build
else
  record_skip "SolidJS workspace typecheck" "complete platform repository is not present"
  record_skip "SolidJS workspace unit tests" "complete platform repository is not present"
  record_skip "SolidJS Portal production build" "complete platform repository is not present"
fi
if [[ -n $opencode_test_command && -n $opencode_test_entry ]]; then
  run_step "Real OpenCode source harness E2E" opencode_source_e2e
  if ((real_deepseek_acp == 1)); then
    run_step "Real OpenCode ACP -> Go ADK -> DeepSeek E2E" real_deepseek_acp_e2e
  fi
else
  record_skip "Real OpenCode source harness E2E" "external OpenCode source paths are not configured"
fi
run_step "Compiled live server, security and load probes" live_server_smoke

if ((docker_audit_enabled == 1)); then
  run_step "Compose validation and production image builds" docker_audit
fi

exit "$overall_status"
