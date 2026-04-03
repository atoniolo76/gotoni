#!/usr/bin/env bash
# GuideLLM against gotoni proxy (OpenAI-compatible /v1).
#
# Autoresearch mode (default): runs one GuideLLM benchmark per routing policy
# (gorgo → least-load → prefix-tree). Before/after each run: POST /proxy/cache/clear
# (proxy prefix-tree state; does not flush SGLang GPU KV — that is server-side).
#
# Usage:
#   export PROXY_TARGET=https://YOUR_TUNNEL.r###.modal.host/v1
#   ./run_guidellm_proxy.sh
#
# Single run (legacy, no policy loop):
#   GUIDELLM_AUTORESEARCH=0 ./run_guidellm_proxy.sh
#
# Optional env:
#   PROXY_TARGET          OpenAI base URL (must end in /v1)
#   GUIDELLM_AUTORESEARCH 1 (default) = policy sweep; 0 = single run
#   GUIDELLM_POLICIES     space-separated list (default: gorgo least-load prefix-tree)
#   GUIDELLM_MODEL        Must match SGLang --model-path
#   GUIDELLM_PROFILE      default: concurrent
#   GUIDELLM_RATE         concurrent = parallel workers (default: 25)
#   GUIDELLM_MAX_SECONDS  default: 60
#   GUIDELLM_DATA_SAMPLES default: -1 (all rows in wildchat_guidellm.jsonl)
#   GUIDELLM_RANDOM_SEED   default: 42 (same seed for every policy run = comparable order)
#   GUIDELLM_OUTPUT_DIR     default: ./guidellm_results_proxy
#   CURL_EXTRA            extra curl flags for proxy control (e.g. -k for TLS)
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
RATE="${GUIDELLM_RATE:-25}"
MAX_SECONDS="${GUIDELLM_MAX_SECONDS:-60}"
DATA_SAMPLES="${GUIDELLM_DATA_SAMPLES:--1}"
RANDOM_SEED="${GUIDELLM_RANDOM_SEED:-42}"
OUT="${GUIDELLM_OUTPUT_DIR:-./guidellm_results_proxy}"
AUTORESEARCH="${GUIDELLM_AUTORESEARCH:-1}"
# shellcheck disable=SC2206
POLICIES=(${GUIDELLM_POLICIES:-gorgo least-load prefix-tree})
CURL_EXTRA=${CURL_EXTRA:--skS}

PROXY_BASE="${PROXY_TARGET%/v1}"
PROXY_BASE="${PROXY_BASE%/}"

BACKEND_KWARGS="${GUIDELLM_BACKEND_KWARGS:-}"
if [[ -z "$BACKEND_KWARGS" ]]; then
  BACKEND_KWARGS='{"timeout":600,"validate_backend":false}'
fi

proxy_cache_clear() {
  echo "  [proxy] POST ${PROXY_BASE}/proxy/cache/clear"
  curl $CURL_EXTRA -X POST "${PROXY_BASE}/proxy/cache/clear" \
    -o /tmp/gotoni_cache_clear.txt -w "  cache/clear HTTP %{http_code}\n" || true
  [[ -f /tmp/gotoni_cache_clear.txt ]] && head -1 /tmp/gotoni_cache_clear.txt | sed 's/^/  /' || true
}

proxy_set_policy() {
  local pol="$1"
  echo "  [proxy] set policy -> ${pol}"
  curl $CURL_EXTRA -X POST "${PROXY_BASE}/proxy/policy" \
    -H "Content-Type: application/json" \
    -d "{\"policy\":\"${pol}\"}" -w "\n  policy HTTP %{http_code}\n"
}

run_guidellm() {
  local run_out="$1"
  shift
  mkdir -p "$run_out"
  echo "  [guidellm] output -> ${run_out}"
  guidellm benchmark run \
    --target "$PROXY_TARGET" \
    --model "$MODEL" \
    --profile "$PROFILE" \
    --rate "$RATE" \
    --max-seconds "$MAX_SECONDS" \
    --data wildchat_guidellm.jsonl \
    --data-column-mapper '{"text_column": "prompt"}' \
    --data-samples "$DATA_SAMPLES" \
    --random-seed "$RANDOM_SEED" \
    --output-dir "$run_out" \
    --outputs json \
    --request-type chat_completions \
    --backend-kwargs "$BACKEND_KWARGS" \
    --disable-console-interactive \
    "$@"
}

mkdir -p "$OUT"

echo "Target:         $PROXY_TARGET"
echo "Proxy control:  $PROXY_BASE"
echo "Model:          $MODEL"
echo "Profile:        $PROFILE  rate=$RATE  max_seconds=$MAX_SECONDS  samples=$DATA_SAMPLES"
echo "Output root:    $OUT"
echo "Random seed:    $RANDOM_SEED"
echo "Autoresearch:   $AUTORESEARCH"
echo ""

if [[ "$AUTORESEARCH" != "1" ]]; then
  echo "Single run (GUIDELLM_AUTORESEARCH=0)"
  run_guidellm "$OUT" "$@"
  exit 0
fi

i=0
for pol in "${POLICIES[@]}"; do
  i=$((i + 1))
  run_dir="${OUT}/run_${i}_${pol}"
  echo "========== Run ${i}/${#POLICIES[@]} policy=${pol} =========="
  proxy_set_policy "$pol"
  echo "  (prefix cache flush before benchmark)"
  proxy_cache_clear
  run_guidellm "$run_dir" "$@"
  echo "  (prefix cache flush after benchmark)"
  proxy_cache_clear
  echo ""
done

echo "Autoresearch sweep complete. Results under: $OUT"
