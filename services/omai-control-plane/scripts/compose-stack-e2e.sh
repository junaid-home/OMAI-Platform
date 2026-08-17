#!/usr/bin/env bash

set -euo pipefail

api_url=${OMAI_E2E_API_URL:-http://omai:8787}
grpc_addr=${OMAI_E2E_GRPC_ADDR:-omai:8787}
app_url=${OMAI_E2E_APP_URL:-http://omai-app}
voice_url=${OMAI_E2E_VOICE_URL:-http://voice:8791}
executor_url=${OMAI_E2E_EXECUTOR_URL:-http://executor:8792}
adk_url=${OMAI_E2E_ADK_URL:-http://adk:8790}
token=${OMAI_E2E_API_TOKEN-}
service_token=${OMAI_E2E_SERVICE_TOKEN-}
runtime_token=${OMAI_E2E_RUNTIME_TOKEN-}
workspace_root=${OMAI_E2E_WORKSPACE_ROOT:-/workspaces}
output_dir=""
verify_session=""
temporary_dir=""

cleanup() {
  if [[ -n $temporary_dir && -d $temporary_dir ]]; then
    rm -rf -- "$temporary_dir"
  fi
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
Usage: compose-stack-e2e.sh --output DIR [--verify-session SESSION_ID]

The create phase proves live service wiring, a Redis-backed deterministic turn,
native gRPC reflection and terminal execution through the private executor. The
verify phase is run after the control plane restarts and proves Redis recovery.
EOF
}

while (($# > 0)); do
  case "$1" in
    --output)
      shift
      output_dir=${1-}
      ;;
    --verify-session)
      shift
      verify_session=${1-}
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 2
      ;;
  esac
  shift
done

if [[ -z $output_dir || -z $token || -z $service_token || -z $runtime_token ]]; then
  echo "--output and the API, service and runtime E2E tokens are required" >&2
  exit 2
fi
if [[ -n $verify_session && ! $verify_session =~ ^[A-Za-z0-9_-]{1,256}$ ]]; then
  echo "--verify-session contains an invalid identifier" >&2
  exit 2
fi
mkdir -p "$output_dir"
temporary_dir=$(mktemp -d /tmp/omai-compose-e2e.XXXXXX)

connect_post() {
  connect_post_as "$token" "$@"
}

connect_post_as() {
  local credential=$1
  shift
  local procedure=$1
  local body=$2
  local destination=$3
  local status
  status=$(curl --silent --show-error --output "$destination" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header 'Connect-Protocol-Version: 1' \
    --header "Authorization: Bearer $credential" \
    --data-binary "$body" "$api_url/$procedure")
  if [[ $status != 200 ]]; then
    echo "ConnectRPC $procedure returned HTTP $status" >&2
    cat "$destination" >&2
    return 1
  fi
}

connect_post 'uab.v1.ControlPlaneService/Health' '{}' "$output_dir/health.json"
connect_post 'uab.v1.ControlPlaneService/ListRuntimes' '{}' "$output_dir/runtimes.json"
connect_post 'uab.v1.ModelCatalogService/ListModels' '{}' "$output_dir/models.json"
python3 - "$output_dir/health.json" "$output_dir/runtimes.json" "$output_dir/models.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    health = json.load(source)
with open(sys.argv[2], encoding="utf-8") as source:
    runtimes = json.load(source)
with open(sys.argv[3], encoding="utf-8") as source:
    models = json.load(source)
ids = {item.get("id") for item in runtimes.get("runtimes", [])}
assert health.get("ok") is True, health
assert {"go-adk", "go-adk-demo"}.issubset(ids), ids
assert len(models.get("models", [])) > 0, models
PY

curl --fail --silent --show-error "$app_url/" --output "$output_dir/portal.html"
curl --fail --silent --show-error "$voice_url/readyz" --output /dev/null
curl --fail --silent --show-error "$executor_url/readyz" --output /dev/null
curl --fail --silent --show-error \
  --header 'Content-Type: application/json' \
  --header 'Connect-Protocol-Version: 1' \
  --header "Authorization: Bearer $runtime_token" \
  --data '{}' "$adk_url/uab.v1.ModelGatewayService/Health" \
  --output "$output_dir/adk-health.json"

grpcurl -plaintext -H "Authorization: Bearer $token" "$grpc_addr" list \
  > "$output_dir/reflection.txt"
grep -qx 'omai.platform.v1.SessionService' "$output_dir/reflection.txt"
grep -qx 'uab.v1.TerminalService' "$output_dir/reflection.txt"
grep -qx 'uab.v1.VoiceControlService' "$output_dir/reflection.txt"

if [[ -n $verify_session ]]; then
  connect_post 'uab.v1.ConversationService/ListMessages' \
    "{\"sessionId\":\"$verify_session\"}" "$output_dir/messages-after-restart.json"
  python3 - "$output_dir/messages-after-restart.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assistant = "".join(
    item.get("text", "") for item in value.get("messages", []) if item.get("role") == "assistant"
)
assert "OMAI demo runtime received" in assistant, value
PY
  echo "verified Redis-backed conversation after control-plane restart"
  exit 0
fi

outside_status=$(curl --silent --output "$output_dir/outside-workspace.json" --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --header 'Connect-Protocol-Version: 1' \
  --header "Authorization: Bearer $token" \
  --data '{"root":"/etc","name":"forbidden"}' \
  "$api_url/omai.platform.v1.ProjectService/ResolveProject")
if [[ $outside_status == 200 ]]; then
  echo "workspace containment accepted /etc" >&2
  exit 1
fi

python3 - "$workspace_root" > "$output_dir/prompt-request.json" <<'PY'
import json
import sys
import time

json.dump({
    "runtimeId": "go-adk-demo",
    "externalSessionId": f"compose-e2e-{time.time_ns()}",
    "root": sys.argv[1],
    "text": "Return the deterministic OMAI Compose E2E response.",
    "title": "Compose E2E",
}, sys.stdout)
PY
connect_post 'uab.v1.ControlPlaneService/Prompt' \
  "@$output_dir/prompt-request.json" "$output_dir/prompt.json"

readarray -t identities < <(python3 - "$output_dir/prompt.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert value.get("accepted") is True, value
print(value["sessionId"])
print(value["workspaceId"])
PY
)
session_id=${identities[0]}
workspace_id=${identities[1]}
printf '%s\n' "$session_id" > "$output_dir/session-id.txt"

assistant_ready=0
for _ in $(seq 1 80); do
  connect_post 'uab.v1.ConversationService/ListMessages' \
    "{\"sessionId\":\"$session_id\"}" "$output_dir/messages.json"
  if python3 - "$output_dir/messages.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assistant = "".join(
    item.get("text", "") for item in value.get("messages", []) if item.get("role") == "assistant"
)
raise SystemExit(0 if "OMAI demo runtime received" in assistant else 1)
PY
  then
    assistant_ready=1
    break
  fi
  sleep 0.1
done
if ((assistant_ready == 0)); then
  echo "deterministic assistant response was not persisted" >&2
  exit 1
fi

python3 - "$workspace_id" > "$temporary_dir/workspace-write-request.json" <<'PY'
import base64
import json
import sys

json.dump({
    "workspaceId": sys.argv[1],
    "path": "e2e/owned-by-go.txt",
    "data": base64.b64encode(b"OMAI Go owns workspace bytes.\n").decode(),
}, sys.stdout)
PY
connect_post 'uab.v1.WorkspaceService/WriteFile' \
  "@$temporary_dir/workspace-write-request.json" "$output_dir/workspace-write.json"
connect_post 'uab.v1.WorkspaceService/ReadFile' \
  "{\"workspaceId\":\"$workspace_id\",\"path\":\"e2e/owned-by-go.txt\"}" \
  "$output_dir/workspace-read.json"
connect_post 'uab.v1.WorkspaceService/SearchFiles' \
  "{\"workspaceId\":\"$workspace_id\",\"query\":\"owned-by-go\",\"limit\":10,\"kind\":\"FILE_SEARCH_KIND_FILE\"}" \
  "$output_dir/workspace-search-files.json"
connect_post 'uab.v1.WorkspaceService/SearchText' \
  "{\"workspaceId\":\"$workspace_id\",\"query\":\"owns workspace\",\"limit\":10}" \
  "$output_dir/workspace-search-text.json"
python3 - "$output_dir/workspace-read.json" "$output_dir/workspace-search-files.json" \
  "$output_dir/workspace-search-text.json" <<'PY'
import base64
import json
import sys

def read(path):
    with open(path, encoding="utf-8") as source:
        return json.load(source)

content, files, matches = (read(path) for path in sys.argv[1:])
assert base64.b64decode(content["data"]) == b"OMAI Go owns workspace bytes.\n", content
assert "e2e/owned-by-go.txt" in files.get("paths", []), files
assert any(item.get("path") == "e2e/owned-by-go.txt" for item in matches.get("matches", [])), matches
PY

connect_post 'uab.v1.GitService/Init' \
  "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/git-init.json"
connect_post 'uab.v1.GitService/Status' \
  "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/git-status.json"
python3 - "$output_dir/git-status.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
files = value.get("status", {}).get("files", [])
assert any(item.get("path") == "e2e/owned-by-go.txt" for item in files), value
PY

connect_post 'uab.v1.LSPService/List' \
  "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/lsp-inventory.json"
connect_post 'uab.v1.MCPService/Upsert' \
  "{\"workspaceId\":\"$workspace_id\",\"server\":{\"id\":\"compose-e2e\",\"name\":\"Compose E2E\",\"transport\":\"stdio\",\"command\":\"/bin/false\",\"enabled\":false}}" \
  "$output_dir/mcp-upsert.json"
connect_post 'uab.v1.MCPService/List' \
  "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/mcp-list.json"
connect_post 'uab.v1.MCPService/Delete' \
  "{\"workspaceId\":\"$workspace_id\",\"serverId\":\"compose-e2e\"}" \
  "$output_dir/mcp-delete.json"
python3 - "$output_dir/lsp-inventory.json" "$output_dir/mcp-list.json" \
  "$output_dir/mcp-delete.json" <<'PY'
import json
import sys

def read(path):
    with open(path, encoding="utf-8") as source:
        return json.load(source)

lsp, mcp, deleted = (read(path) for path in sys.argv[1:])
assert isinstance(lsp.get("servers", []), list), lsp
assert any(item.get("id") == "compose-e2e" and item.get("enabled") is False for item in mcp.get("servers", [])), mcp
assert deleted.get("deleted") is True, deleted
PY

python3 - "$workspace_id" > "$temporary_dir/preview-index-request.json" <<'PY'
import base64
import json
import sys

json.dump({
    "workspaceId": sys.argv[1],
    "path": "index.html",
    "data": base64.b64encode(b"<!doctype html><h1>OMAI Compose Preview</h1>\n").decode(),
}, sys.stdout)
PY
connect_post 'uab.v1.WorkspaceService/WriteFile' \
  "@$temporary_dir/preview-index-request.json" "$output_dir/preview-index-write.json"
connect_post 'uab.v1.PreviewService/Analyze' \
  "{\"root\":\"$workspace_root\"}" "$output_dir/preview-analyze.json"
connect_post 'uab.v1.PreviewService/Start' \
  "{\"root\":\"$workspace_root\"}" "$temporary_dir/preview-start.json"
preview_url=$(python3 - "$temporary_dir/preview-start.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
preview = value.get("preview", {})
assert preview.get("status") == "ready", value
assert preview.get("port", 0) > 0, value
assert preview.get("publicUrl", "").startswith("http://omai:8787/__omai/preview/"), value
print(preview["publicUrl"])
PY
)
curl --fail --silent --show-error "$preview_url" --output "$output_dir/preview-page.html"
grep -q 'OMAI Compose Preview' "$output_dir/preview-page.html"
connect_post 'uab.v1.PreviewService/Stop' \
  "{\"workspaceId\":\"$workspace_id\"}" "$output_dir/preview-stop.json"
preview_after_stop_status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' "$preview_url")
if [[ $preview_after_stop_status != 404 ]]; then
  echo "retired preview capability returned HTTP $preview_after_stop_status, want 404" >&2
  exit 1
fi
python3 - "$temporary_dir/preview-start.json" "$output_dir/preview-stop.json" \
  "$preview_after_stop_status" > "$output_dir/preview-control.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    start = json.load(source)["preview"]
with open(sys.argv[2], encoding="utf-8") as source:
    stop = json.load(source)
assert stop.get("stopped") is True, stop
json.dump({
    "framework": start.get("framework"),
    "status": start.get("status"),
    "port_allocated": start.get("port", 0) > 0,
    "gateway_content_verified": True,
    "stopped": True,
    "retired_capability_http_status": int(sys.argv[3]),
    "capability_redacted": True,
}, sys.stdout, indent=2)
print()
PY

python3 - "$workspace_id" > "$temporary_dir/voice-mint-request.json" <<'PY'
import json
import sys

json.dump({"workspaceId": sys.argv[1], "locale": "de-CH", "voice": "Kore"}, sys.stdout)
PY
connect_post 'uab.v1.VoiceControlService/MintTicket' \
  "@$temporary_dir/voice-mint-request.json" "$temporary_dir/voice-mint.json"
voice_ticket=$(python3 - "$temporary_dir/voice-mint.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert value.get("websocketPath") == "/omai/voice/ws", value
assert value.get("expiresUnixMillis", 0) > 0, value
print(value["ticket"])
PY
)
python3 - "$voice_ticket" > "$temporary_dir/voice-redeem-request.json" <<'PY'
import json
import sys

json.dump({"ticket": sys.argv[1], "sessionId": "compose-e2e-voice"}, sys.stdout)
PY
connect_post_as "$service_token" 'uab.v1.VoiceControlService/RedeemTicket' \
  "@$temporary_dir/voice-redeem-request.json" "$temporary_dir/voice-redeem.json"
replay_status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --header 'Connect-Protocol-Version: 1' \
  --header "Authorization: Bearer $service_token" \
  --data-binary "@$temporary_dir/voice-redeem-request.json" \
  "$api_url/uab.v1.VoiceControlService/RedeemTicket")
if [[ $replay_status == 200 ]]; then
  echo "one-time voice ticket replay was accepted" >&2
  exit 1
fi
voice_lease=$(python3 - "$temporary_dir/voice-redeem.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
assert value.get("leaseExpiresUnixMillis", 0) > 0, value
assert value.get("claims", {}).get("workspaceId"), value
print(value["leaseToken"])
PY
)
python3 - "$voice_lease" > "$temporary_dir/voice-lease-request.json" <<'PY'
import json
import sys

json.dump({"leaseToken": sys.argv[1]}, sys.stdout)
PY
connect_post_as "$service_token" 'uab.v1.VoiceControlService/Heartbeat' \
  "@$temporary_dir/voice-lease-request.json" "$temporary_dir/voice-heartbeat.json"
connect_post_as "$service_token" 'uab.v1.VoiceControlService/ListVoiceTools' \
  "@$temporary_dir/voice-lease-request.json" "$temporary_dir/voice-tools.json"
connect_post_as "$service_token" 'uab.v1.VoiceControlService/Release' \
  "@$temporary_dir/voice-lease-request.json" "$temporary_dir/voice-release.json"
python3 - "$temporary_dir/voice-heartbeat.json" "$temporary_dir/voice-tools.json" \
  "$temporary_dir/voice-release.json" "$replay_status" > "$output_dir/voice-control.json" <<'PY'
import json
import sys

def read(path):
    with open(path, encoding="utf-8") as source:
        return json.load(source)

heartbeat, tools, release = (read(path) for path in sys.argv[1:4])
assert heartbeat.get("active") is True, heartbeat
assert tools.get("registryEtag"), tools
assert len(tools.get("tools", [])) > 0, tools
assert release.get("active") is False, release
json.dump({
    "ticket_minted": True,
    "ticket_redeemed_once": True,
    "ticket_replay_http_status": int(sys.argv[4]),
    "lease_heartbeat": True,
    "reflected_tool_count": len(tools["tools"]),
    "lease_released": True,
    "credentials_redacted": True,
}, sys.stdout, indent=2)
print()
PY

connect_post 'uab.v1.TerminalService/Create' \
  "{\"workspaceId\":\"$workspace_id\",\"command\":\"/bin/sh\",\"args\":[\"-c\",\"printf omai-compose-terminal\"]}" \
  "$output_dir/terminal-create.json"
terminal_id=$(python3 - "$output_dir/terminal-create.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    print(json.load(source)["terminal"]["id"])
PY
)
grpcurl -plaintext -H "Authorization: Bearer $token" \
  -d "{\"terminalId\":\"$terminal_id\",\"cursor\":\"0\"}" \
  "$grpc_addr" uab.v1.TerminalService/Watch > "$output_dir/terminal-watch.jsonstream"
grep -q 'b21haS1jb21wb3NlLXRlcm1pbmFs' "$output_dir/terminal-watch.jsonstream"
grep -q '"exited": true' "$output_dir/terminal-watch.jsonstream"
connect_post 'uab.v1.TerminalService/Remove' \
  "{\"terminalId\":\"$terminal_id\"}" "$output_dir/terminal-remove.json"

echo "compose stack, Redis state, native gRPC and private executor E2E completed"
