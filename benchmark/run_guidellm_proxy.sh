#!/usr/bin/env bash
# GuideLLM against gotoni proxy (OpenAI-compatible /v1).
#
# Usage:
#   export PROXY_TARGET=https://YOUR_TUNNEL.r###.modal.host/v1
#   ./run_guidellm_proxy.sh
#
# Optional env:
#   PROXY_TARGET     OpenAI base URL (must end in /v1), e.g. from sandbox_info.json
#                    proxy-us-east tunnels["8000"] + "/v1"
#   GUIDELLM_MODEL   Must match SGLang --model-path (default: Mistral-7B-Instruct)
#   GUIDELLM_PROFILE concurrent|constant|poisson|... (default: concurrent)
#   GUIDELLM_RATE    concurrent = number of parallel requests (default: 4)
#   GUIDELLM_MAX_SECONDS  (default: 120)
#   GUIDELLM_DATA_SAMPLES   (default: 40; use -1 for full file)
#   GUIDELLM_OUTPUT_DIR  (default: ./guidellm_results_proxy)
#
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

PROXY_TARGET="${PROXY_TARGET:-}"
if [[ -z "$PROXY_TARGET" ]]; then
  echo "Set PROXY_TARGET to your proxy tunnel OpenAI base URL, e.g.:" >&2
  echo '  export PROXY_TARGET="https://<proxy-tunnel>.r###.modal.host/v1"' >&2
  echo "(from sandbox_info.json → proxy-us-east → tunnels[\"8000\"] + /v1)" >&2
  exit 1
fi

MODEL="${GUIDELLM_MODEL:-mistralai/Mistral-7B-Instruct-v0.3}"
PROFILE="${GUIDELLM_PROFILE:-concurrent}"
RATE="${GUIDELLM_RATE:-4}"
MAX_SECONDS="${GUIDELLM_MAX_SECONDS:-120}"
DATA_SAMPLES="${GUIDELLM_DATA_SAMPLES:-40}"
OUT="${GUIDELLM_OUTPUT_DIR:-./guidellm_results_proxy}"

# GuideLLM's default preflight is GET {base}/health (gotoni uses /proxy/health, or /health on newer proxies).
# Preflight expects HTTP 2xx; if all backends are unhealthy /proxy/health returns 503 and GuideLLM exits.
# Default: skip validation so the run starts; completions still need healthy backends + /metrics.
# Strict check: GUIDELLM_BACKEND_KWARGS='{"timeout":600,"validate_backend":{"url":"https://<host>/proxy/health","method":"GET"}}'
PROXY_BASE="${PROXY_TARGET%/v1}"
PROXY_BASE="${PROXY_BASE%/}"
BACKEND_KWARGS="${GUIDELLM_BACKEND_KWARGS:-}"
if [[ -z "$BACKEND_KWARGS" ]]; then
  BACKEND_KWARGS='{"timeout":600,"validate_backend":false}'
fi

mkdir -p "$OUT"

echo "Target:    $PROXY_TARGET"
echo "Model:     $MODEL"
echo "Profile:   $PROFILE  rate=$RATE  max_seconds=$MAX_SECONDS  samples=$DATA_SAMPLES"
echo "Output:    $OUT"
echo ""

exec guidellm benchmark run \
  --target "$PROXY_TARGET" \
  --model "$MODEL" \
  --profile "$PROFILE" \
  --rate "$RATE" \
  --max-seconds "$MAX_SECONDS" \
  --data wildchat_guidellm.jsonl \
  --data-column-mapper '{"text_column": "prompt"}' \
  --data-samples "$DATA_SAMPLES" \
  --output-dir "$OUT" \
  --outputs json \
  --request-type chat_completions \
  --backend-kwargs "$BACKEND_KWARGS" \
  --disable-console-interactive \
  "$@"
