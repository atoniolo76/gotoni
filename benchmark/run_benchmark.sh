#!/bin/bash
# run_benchmark.sh - Complete cluster verification and benchmark runner
#
# This script:
# 1. Verifies cluster instances are running
# 2. Checks/uploads gotoni binary to all nodes
# 3. Verifies SGLang is running on all nodes
# 4. Verifies/starts load balancers on all nodes
# 5. Runs the benchmark
#
# Usage: ./run_benchmark.sh [--skip-upload] [--skip-lb] [--strategy gorgo|prefix-tree|least-loaded]

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default settings
SKIP_UPLOAD=false
SKIP_LB=false
STRATEGY="gorgo"
MAX_CONCURRENT=100
BENCHMARK_DURATION=60
NUM_USERS=32

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-upload)
            SKIP_UPLOAD=true
            shift
            ;;
        --skip-lb)
            SKIP_LB=true
            shift
            ;;
        --strategy)
            STRATEGY="$2"
            shift 2
            ;;
        --duration)
            BENCHMARK_DURATION="$2"
            shift 2
            ;;
        --users)
            NUM_USERS="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [options]"
            echo ""
            echo "Options:"
            echo "  --skip-upload    Skip building and uploading gotoni binary"
            echo "  --skip-lb        Skip load balancer verification/restart"
            echo "  --strategy       LB strategy: gorgo, prefix-tree, least-loaded (default: gorgo)"
            echo "  --duration       Benchmark duration in seconds (default: 60)"
            echo "  --users          Number of concurrent users (default: 32)"
            echo "  --help           Show this help"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Load environment
if [ -f .env ]; then
    export $(cat .env | grep -v '^#' | xargs)
fi

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}       GOTONI CLUSTER BENCHMARK RUNNER${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Strategy:     $STRATEGY"
echo "Duration:     ${BENCHMARK_DURATION}s"
echo "Users:        $NUM_USERS"
echo ""

# ============================================================================
# STEP 1: Verify cluster instances
# ============================================================================
echo -e "${YELLOW}━━━ Step 1: Checking Cluster Instances ━━━${NC}"

INSTANCES=$(./gotoni list 2>/dev/null | grep -E "^Name:|^IP:" | paste - - | awk '{print $2, $4}')
if [ -z "$INSTANCES" ]; then
    echo -e "${RED}✗ No running instances found!${NC}"
    exit 1
fi

INSTANCE_COUNT=$(echo "$INSTANCES" | wc -l | tr -d ' ')
echo -e "${GREEN}✓ Found $INSTANCE_COUNT running instances:${NC}"

declare -a NAMES
declare -a IPS

while read -r name ip; do
    NAMES+=("$name")
    IPS+=("$ip")
    echo "  • $name ($ip)"
done <<< "$INSTANCES"

echo ""

# ============================================================================
# STEP 2: Build and upload gotoni binary
# ============================================================================
if [ "$SKIP_UPLOAD" = false ]; then
    echo -e "${YELLOW}━━━ Step 2: Building and Uploading Binary ━━━${NC}"
    
    echo "Building gotoni for Linux..."
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/gotoni-linux .
    
    if [ ! -f /tmp/gotoni-linux ]; then
        echo -e "${RED}✗ Build failed!${NC}"
        exit 1
    fi
    
    BINARY_SIZE=$(ls -lh /tmp/gotoni-linux | awk '{print $5}')
    echo -e "${GREEN}✓ Built gotoni-linux ($BINARY_SIZE)${NC}"
    
    # Start local HTTP server for download
    echo "Starting temporary file server..."
    mkdir -p /tmp/serve_bin
    cp /tmp/gotoni-linux /tmp/serve_bin/gotoni
    
    # Kill any existing server
    pkill -f "python3 -m http.server 9998" 2>/dev/null || true
    
    cd /tmp/serve_bin
    python3 -m http.server 9998 &>/dev/null &
    SERVER_PID=$!
    cd - >/dev/null
    
    # Start bore tunnel
    pkill -f "bore local 9998" 2>/dev/null || true
    bore local 9998 --to bore.pub &>/dev/null &
    BORE_PID=$!
    
    sleep 5
    
    # Get bore port from log (hacky but works)
    BORE_PORT=$(ps aux | grep "bore local" | grep -v grep | head -1 | grep -oE ":[0-9]+" | tail -1 | tr -d ':')
    if [ -z "$BORE_PORT" ]; then
        # Try to get it from bore's output
        sleep 2
        BORE_PORT=25758  # fallback
    fi
    
    DOWNLOAD_URL="http://bore.pub:$BORE_PORT/gotoni"
    echo "Download URL: $DOWNLOAD_URL"
    
    # Download to all instances
    for i in "${!NAMES[@]}"; do
        name="${NAMES[$i]}"
        ip="${IPS[$i]}"
        echo -n "  Uploading to $name... "
        ./gotoni run "$name" "curl -sL '$DOWNLOAD_URL' -o /home/ubuntu/gotoni_new && chmod +x /home/ubuntu/gotoni_new && mv /home/ubuntu/gotoni_new /home/ubuntu/gotoni" 2>/dev/null
        
        # Verify
        size=$(./gotoni run "$name" "ls -la /home/ubuntu/gotoni 2>/dev/null | awk '{print \$5}'" 2>/dev/null | tail -1)
        if [ -n "$size" ] && [ "$size" -gt 1000000 ]; then
            echo -e "${GREEN}✓ ($size bytes)${NC}"
        else
            echo -e "${RED}✗ Upload failed${NC}"
        fi
    done
    
    # Cleanup
    kill $SERVER_PID 2>/dev/null || true
    kill $BORE_PID 2>/dev/null || true
    
    echo ""
else
    echo -e "${YELLOW}━━━ Step 2: Skipping Binary Upload ━━━${NC}"
    echo ""
fi

# ============================================================================
# STEP 3: Verify SGLang is running
# ============================================================================
echo -e "${YELLOW}━━━ Step 3: Checking SGLang Status ━━━${NC}"

SGLANG_OK=0
SGLANG_FAIL=0

for i in "${!NAMES[@]}"; do
    name="${NAMES[$i]}"
    ip="${IPS[$i]}"
    echo -n "  $name ($ip:8080)... "
    
    # Check if SGLang is responding
    http_code=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "http://$ip:8080/health" 2>/dev/null || echo "000")
    
    if [ "$http_code" = "200" ]; then
        # Get running/waiting requests
        metrics=$(curl -s --connect-timeout 5 "http://$ip:8080/metrics" 2>/dev/null | grep -E "sglang:num_(running|waiting)_reqs" | head -2)
        running=$(echo "$metrics" | grep running | grep -oE "[0-9.]+" | head -1)
        waiting=$(echo "$metrics" | grep waiting | grep -oE "[0-9.]+" | head -1)
        echo -e "${GREEN}✓ healthy (running: ${running:-0}, waiting: ${waiting:-0})${NC}"
        ((SGLANG_OK++))
    else
        echo -e "${RED}✗ not responding (code: $http_code)${NC}"
        ((SGLANG_FAIL++))
    fi
done

if [ $SGLANG_FAIL -gt 0 ]; then
    echo ""
    echo -e "${RED}Warning: $SGLANG_FAIL SGLang instances are not healthy!${NC}"
    echo "Run 'gotoni sglang diagnose' for more details."
fi

echo ""

# ============================================================================
# STEP 4: Verify/Start Load Balancers
# ============================================================================
if [ "$SKIP_LB" = false ]; then
    echo -e "${YELLOW}━━━ Step 4: Checking Load Balancer Status ━━━${NC}"
    
    LB_OK=0
    LB_NEEDS_START=()
    
    for i in "${!NAMES[@]}"; do
        name="${NAMES[$i]}"
        ip="${IPS[$i]}"
        echo -n "  $name ($ip:8000)... "
        
        # Check if LB is responding
        lb_status=$(curl -s --connect-timeout 5 "http://$ip:8000/lb/status" 2>/dev/null)
        
        if [ -n "$lb_status" ]; then
            policy=$(echo "$lb_status" | jq -r '.policy' 2>/dev/null)
            peers=$(echo "$lb_status" | jq -r '.peer_count' 2>/dev/null)
            max_load=$(echo "$lb_status" | jq -r '.max_load' 2>/dev/null)
            echo -e "${GREEN}✓ running (policy: $policy, peers: $peers, max_load: $max_load)${NC}"
            ((LB_OK++))
        else
            echo -e "${YELLOW}○ not running${NC}"
            LB_NEEDS_START+=("$name")
        fi
    done
    
    echo ""
    
    # Start LBs if needed
    if [ ${#LB_NEEDS_START[@]} -gt 0 ]; then
        echo -e "${YELLOW}Starting load balancers on ${#LB_NEEDS_START[@]} instances...${NC}"
        
        # Build peer list
        PEER_ARGS=""
        for ip in "${IPS[@]}"; do
            PEER_ARGS="$PEER_ARGS --peers $ip:8000"
        done
        
        for name in "${LB_NEEDS_START[@]}"; do
            echo -n "  Starting LB on $name... "
            
            # Kill any existing
            ./gotoni run "$name" "fuser -k 8000/tcp 2>/dev/null || true" 2>/dev/null
            sleep 1
            
            # Start new LB
            ./gotoni run "$name" "nohup /home/ubuntu/gotoni lb start --listen-port 8000 --local-port 8080 --strategy $STRATEGY --max-concurrent $MAX_CONCURRENT --ie-queue-indicator $PEER_ARGS > /home/ubuntu/lb.log 2>&1 &" 2>/dev/null
            
            sleep 2
            
            # Find the IP for this name
            for j in "${!NAMES[@]}"; do
                if [ "${NAMES[$j]}" = "$name" ]; then
                    check_ip="${IPS[$j]}"
                    break
                fi
            done
            
            # Verify
            lb_health=$(curl -s --connect-timeout 5 "http://$check_ip:8000/lb/health" 2>/dev/null)
            if [ "$lb_health" = "OK" ]; then
                echo -e "${GREEN}✓${NC}"
            else
                echo -e "${RED}✗ failed to start${NC}"
            fi
        done
        
        echo ""
    fi
else
    echo -e "${YELLOW}━━━ Step 4: Skipping Load Balancer Check ━━━${NC}"
    echo ""
fi

# ============================================================================
# STEP 5: Final Status Summary
# ============================================================================
echo -e "${YELLOW}━━━ Step 5: Cluster Summary ━━━${NC}"

echo "Instances:    $INSTANCE_COUNT"
echo "SGLang:       $SGLANG_OK healthy"
echo "Strategy:     $STRATEGY"
echo ""

# Collect final LB status
for i in "${!NAMES[@]}"; do
    name="${NAMES[$i]}"
    ip="${IPS[$i]}"
    lb_info=$(curl -s --connect-timeout 3 "http://$ip:8000/lb/status" 2>/dev/null)
    if [ -n "$lb_info" ]; then
        policy=$(echo "$lb_info" | jq -r '.policy' 2>/dev/null)
        peers=$(echo "$lb_info" | jq -r '.peer_count' 2>/dev/null)
        running=$(echo "$lb_info" | jq -r '.running_reqs' 2>/dev/null)
        waiting=$(echo "$lb_info" | jq -r '.waiting_reqs' 2>/dev/null)
        echo "  $name: policy=$policy peers=$peers running=$running waiting=$waiting"
    else
        echo "  $name: LB not responding"
    fi
done

echo ""

# ============================================================================
# STEP 6: Start Tracing (Optional)
# ============================================================================
echo -e "${YELLOW}━━━ Step 6: Starting Trace Collection ━━━${NC}"

# Start tracing on all nodes
for i in "${!NAMES[@]}"; do
    ip="${IPS[$i]}"
    name="${NAMES[$i]}"
    echo -n "  Starting trace on $name... "
    result=$(curl -s -X POST "http://$ip:8000/lb/trace/start" 2>/dev/null)
    if [ -n "$result" ]; then
        echo -e "${GREEN}✓${NC}"
    else
        echo -e "${YELLOW}○ endpoint not available${NC}"
    fi
done

echo ""

# ============================================================================
# STEP 7: Run Benchmark
# ============================================================================
echo -e "${YELLOW}━━━ Step 7: Running Benchmark ━━━${NC}"
echo "Duration: ${BENCHMARK_DURATION}s | Users: $NUM_USERS | Strategy: $STRATEGY"
echo ""

# Activate venv if exists
if [ -d "venv" ]; then
    source venv/bin/activate
fi

# Build IP arguments based on discovered instances
WEST_COAST_IP=""
GERMANY_IP=""
ISRAEL_IP=""

for i in "${!NAMES[@]}"; do
    name="${NAMES[$i]}"
    ip="${IPS[$i]}"
    case "$name" in
        west_coast|*west*|*california*)
            WEST_COAST_IP="$ip"
            ;;
        *germany*|gotoni-1769464777)
            GERMANY_IP="$ip"
            ;;
        gpu-2|*israel*)
            ISRAEL_IP="$ip"
            ;;
    esac
done

# Construct benchmark command
BENCHMARK_CMD="python benchmark/wildchat.py --duration $BENCHMARK_DURATION --num-users $NUM_USERS --output-dir benchmark_results"

[ -n "$WEST_COAST_IP" ] && BENCHMARK_CMD="$BENCHMARK_CMD --west-coast-ip $WEST_COAST_IP"
[ -n "$GERMANY_IP" ] && BENCHMARK_CMD="$BENCHMARK_CMD --germany-ip $GERMANY_IP"
[ -n "$ISRAEL_IP" ] && BENCHMARK_CMD="$BENCHMARK_CMD --israel-ip $ISRAEL_IP"

echo "Running: $BENCHMARK_CMD"
echo ""

# Run the benchmark
eval $BENCHMARK_CMD

echo ""

# ============================================================================
# STEP 8: Stop Tracing and Collect Results
# ============================================================================
echo -e "${YELLOW}━━━ Step 8: Collecting Trace Data ━━━${NC}"

TRACE_DIR="benchmark_results/traces_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$TRACE_DIR"

for i in "${!NAMES[@]}"; do
    ip="${IPS[$i]}"
    name="${NAMES[$i]}"
    echo -n "  Collecting trace from $name... "
    
    # Stop and get trace
    trace=$(curl -s -X POST "http://$ip:8000/lb/trace/stop" 2>/dev/null)
    if [ -n "$trace" ] && [ "$trace" != "null" ]; then
        echo "$trace" > "$TRACE_DIR/${name}_trace.json"
        events=$(echo "$trace" | jq '.events | length' 2>/dev/null)
        echo -e "${GREEN}✓ ($events events)${NC}"
    else
        echo -e "${YELLOW}○ no trace data${NC}"
    fi
done

echo ""
echo "Traces saved to: $TRACE_DIR/"

# Analyze traces if we have any
if ls "$TRACE_DIR"/*.json 1> /dev/null 2>&1; then
    echo ""
    echo -e "${YELLOW}Analyzing traces...${NC}"
    for tracefile in "$TRACE_DIR"/*.json; do
        echo ""
        echo "=== $(basename $tracefile) ==="
        python benchmark/analyze_trace.py "$tracefile" 2>/dev/null || echo "  (analysis failed)"
    done
fi

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}       BENCHMARK COMPLETE${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Results in: benchmark_results/"
echo "Traces in:  $TRACE_DIR/"
