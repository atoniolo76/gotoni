#!/usr/bin/env bash
# Run Modal CLI diagnostics against sandboxes listed in sandbox_info.json (sandbox_id).
#
#   chmod +x modal_sandbox_debug.sh
#   ./modal_sandbox_debug.sh              # print commands only
#   ./modal_sandbox_debug.sh run proxy    # run checks inside proxy-us-east (needs modal auth)
#
# Environment: MODAL_ENVIRONMENT=alessio-dev (must match launch_sandboxes.py)

set -euo pipefail
cd "$(dirname "$0")"

ENV="${MODAL_ENVIRONMENT:-alessio-dev}"
JSON="sandbox_info.json"

die() { echo "$*" >&2; exit 1; }
[[ -f "$JSON" ]] || die "missing $JSON — run launch_sandboxes.py first"

id_for() {
  python3 -c "import json; d=json.load(open('$JSON')); print(next(x['sandbox_id'] for x in d if x.get('name')=='''$1'''))"
}

PROXY_ID="$(id_for proxy-us-east)"

print_cmds() {
  cat <<EOF
Modal CLI (v1.x): IDs from $JSON are Sandbox IDs (sb-...).

  # List running workers — use "Container ID" (ta-...) for modal container logs, not sb-...
  modal container list -e $ENV --json
  # modal container logs ta-01KN3....   # example

  # Sandbox attach (sb-...): inside the box, only sleep runs until you start gotoni/SGLang.
  # curl 127.0.0.1:8000 failing is expected until: gotoni proxy start --listen-port 8000 ...

  # One-shot commands inside the sandbox (no TTY) — sb-... works for exec on many builds
  modal container exec $PROXY_ID -- ps aux
  modal container exec $PROXY_ID -- sh -c 'ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || true'
  modal container exec $PROXY_ID -- sh -c 'curl -sS -o /dev/null -w "%{http_code}\\n" http://127.0.0.1:8000/proxy/servers || echo curl_failed'

  # Interactive shell on the running Sandbox (debug)
  modal shell $PROXY_ID -e $ENV

  # Non-interactive check that gotoni is listening on 8000
  modal shell $PROXY_ID -e $ENV -c 'curl -sS http://127.0.0.1:8000/proxy/status || true'

If \`modal container exec\` fails with "unknown container", use \`modal shell <sb-id>\` only —
some builds route Sandbox attach via \`modal shell\`, not \`container exec\`.

Tunnel shows TLS OK but "empty reply" usually means: nothing listening on port 8000 inside
the box (gotoni not started), or the public tunnel hostname is stale (re-run launch_sandboxes.py).
EOF
}

run_proxy_checks() {
  echo "=== modal container exec $PROXY_ID (env=$ENV) ==="
  modal container exec "$PROXY_ID" -- sh -c 'echo "--- listeners ---"; (ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null); echo "--- curl localhost:8000/proxy/servers ---"; curl -sS -w "\nhttp:%{http_code}\n" --max-time 10 http://127.0.0.1:8000/proxy/servers || true; echo "--- ps gotoni ---"; ps aux | grep -E "[g]otoni|[s]leep" || true'
}

case "${1:-print}" in
  print|""|help|-h|--help) print_cmds ;;
  run) case "${2:-}" in
    proxy) run_proxy_checks ;;
    *) die "usage: $0 run proxy" ;;
  esac ;;
  *) die "usage: $0 [print|run proxy]" ;;
esac
