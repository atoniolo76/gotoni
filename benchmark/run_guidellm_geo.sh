#!/bin/bash
#
# Run GuideLLM with geo-distributed routing
#
# This script:
# 1. Preprocesses WildChat if needed (generates lookup table)
# 2. Starts the geo-routing proxy
# 3. Runs GuideLLM benchmark through the proxy
# 4. Cleans up and shows results
#
# Usage:
#   ./run_guidellm_geo.sh [options]
#
# Options:
#   --profile PROFILE     Traffic profile: sweep, poisson, constant, concurrent, throughput (default: poisson)
#   --rate RATE           Request rate for constant/poisson/concurrent profiles (default: 5)
#   --duration SECONDS    Benchmark duration in seconds (default: 60)
#   --num-samples NUM     Number of WildChat samples to use (default: 10000)
#   --skip-preprocess     Skip dataset preprocessing (use existing files)
#   --output-dir DIR      Output directory for results (default: ./guidellm_results)
#

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Default options
PROFILE="poisson"
RATE="5"
DURATION="60"
NUM_SAMPLES="10000"
SKIP_PREPROCESS=false
OUTPUT_DIR="./guidellm_results"
PROXY_PORT="9000"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --profile)
            PROFILE="$2"
            shift 2
            ;;
        --rate)
            RATE="$2"
            shift 2
            ;;
        --duration)
            DURATION="$2"
            shift 2
            ;;
        --num-samples)
            NUM_SAMPLES="$2"
            shift 2
            ;;
        --skip-preprocess)
            SKIP_PREPROCESS=true
            shift
            ;;
        --output-dir)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --help|-h)
            head -30 "$0" | tail -20
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║        GuideLLM Geo-Distributed Benchmark                    ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
echo "Configuration:"
echo "  Profile: $PROFILE"
echo "  Rate: $RATE req/s"
echo "  Duration: ${DURATION}s"
echo "  Output: $OUTPUT_DIR"
echo ""

# Step 1: Preprocess dataset if needed
if [ "$SKIP_PREPROCESS" = false ] || [ ! -f "wildchat_guidellm.jsonl" ]; then
    echo "Step 1: Preprocessing WildChat dataset..."
    python preprocess_wildchat_geo.py --num-samples "$NUM_SAMPLES"
    echo ""
else
    echo "Step 1: Skipping preprocessing (using existing files)"
    echo ""
fi

# Verify files exist
if [ ! -f "wildchat_guidellm.jsonl" ] || [ ! -f "wildchat_location_lookup.json" ]; then
    echo "ERROR: Required files not found. Run without --skip-preprocess."
    exit 1
fi

# Step 2: Start geo-proxy in background
echo "Step 2: Starting geo-routing proxy on port $PROXY_PORT..."
python geo_proxy.py --port "$PROXY_PORT" &
PROXY_PID=$!

# Wait for proxy to start
sleep 2

# Check if proxy is running
if ! kill -0 $PROXY_PID 2>/dev/null; then
    echo "ERROR: Failed to start geo-proxy"
    exit 1
fi

echo "  Proxy PID: $PROXY_PID"
echo ""

# Cleanup function
cleanup() {
    echo ""
    echo "Stopping geo-proxy (PID: $PROXY_PID)..."
    kill $PROXY_PID 2>/dev/null || true
    wait $PROXY_PID 2>/dev/null || true
}
trap cleanup EXIT

# Step 3: Run GuideLLM benchmark
echo "Step 3: Running GuideLLM benchmark..."
echo ""

mkdir -p "$OUTPUT_DIR"

# Build GuideLLM command
GUIDELLM_CMD="guidellm benchmark \
    --target http://localhost:$PROXY_PORT/v1 \
    --profile $PROFILE \
    --max-seconds $DURATION \
    --data $SCRIPT_DIR/wildchat_guidellm.jsonl \
    --data-column-mapper '{\"text_column\": \"prompt\"}' \
    --output-path $OUTPUT_DIR"

# Add rate for profiles that need it
if [ "$PROFILE" = "poisson" ] || [ "$PROFILE" = "constant" ] || [ "$PROFILE" = "concurrent" ] || [ "$PROFILE" = "throughput" ]; then
    GUIDELLM_CMD="$GUIDELLM_CMD --rate $RATE"
fi

echo "Running: $GUIDELLM_CMD"
echo ""

eval $GUIDELLM_CMD

# Step 4: Show summary
echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║                     BENCHMARK COMPLETE                       ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
echo "Results saved to: $OUTPUT_DIR"
echo ""

# Show routing distribution from logs
if [ -f "geo_proxy_routing.log" ]; then
    echo "Routing distribution:"
    cat geo_proxy_routing.log | grep '"routed_to"' | \
        sed 's/.*"routed_to": *"\([^"]*\)".*/\1/' | \
        sort | uniq -c | sort -rn
    echo ""
fi

echo "View detailed results:"
echo "  - HTML report: $OUTPUT_DIR/benchmarks.html"
echo "  - JSON data: $OUTPUT_DIR/benchmarks.json"
echo "  - Routing log: geo_proxy_routing.log"
echo ""
echo "Analyze routing log with:"
echo "  cat geo_proxy_routing.log | jq -s 'group_by(.routed_to) | map({node: .[0].routed_to, count: length, avg_latency_ms: (map(.total_latency_ms) | add / length)})'"
