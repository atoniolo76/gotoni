#!/usr/bin/env bash
# GuideLLM against gotoni proxy (OpenAI-compatible /v1).
#
# Usage:
#   export PROXY_TARGET=https://YOUR_TUNNEL.r###.modal.host/v1
#   ./run_guidellm_proxy.sh
#
# Optional env:
#   PROXY_TARGET     (default below — override with your proxy :8000 tunnel)
#   GUIDELLM_PROFILE concurrent|constant|poisson|... (default: concurrent)
#   GUIDELLM_RATE    concurrent = number of parallel requests (default: 4)
#   GUIDELLM_MAX_SECONDS  (default: 120)
#   GUIDELLM_DATA_SAMPLES   (default: 40; use -1 for full file)
#   GUIDELLM_OUTPUT_DIR  (default: ./guidellm_results_proxy)
#
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

PROXY_TARGET="${PROXY_TARGET:-https://2pq3ufq0m8b8r7.r444.modal.host/v1}"
PROFILE="${GUIDELLM_PROFILE:-concurrent}"
RATE="${GUIDELLM_RATE:-4}"
MAX_SECONDS="${GUIDELLM_MAX_SECONDS:-120}"
DATA_SAMPLES="${GUIDELLM_DATA_SAMPLES:-40}"
OUT="${GUIDELLM_OUTPUT_DIR:-./guidellm_results_proxy}"

mkdir -p "$OUT"

echo "Target:    $PROXY_TARGET"
echo "Profile:   $PROFILE  rate=$RATE  max_seconds=$MAX_SECONDS  samples=$DATA_SAMPLES"
echo "Output:    $OUT"
echo ""

exec guidellm benchmark run \
  --target "$PROXY_TARGET" \
  --profile "$PROFILE" \
  --rate "$RATE" \
  --max-seconds "$MAX_SECONDS" \
  --data wildchat_guidellm.jsonl \
  --data-column-mapper '{"text_column": "prompt"}' \
  --data-samples "$DATA_SAMPLES" \
  --output-dir "$OUT" \
  --outputs json \
  --request-type chat_completions \
  --backend-kwargs '{"timeout": 600}' \
  --disable-console-interactive \
  "$@"
