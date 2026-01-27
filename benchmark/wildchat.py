"""WildChat-1M multi-turn conversation workload with synthetic timestamps.

Runs benchmark through gotoni load balancer with configurable policies.
"""

import argparse
import asyncio
import collections
import json
import os
import sqlite3
import statistics
import time
from dataclasses import dataclass, field, asdict
from typing import Any, Dict, List, Optional, Tuple

import aiohttp
import datasets
from openai import AsyncOpenAI

DATASET_NAME = 'allenai/WildChat-1M'

# Default cluster name used by TestClusterSGLang
DEFAULT_CLUSTER_NAME = 'sglang-auto-cluster'

# Default load balancer port
DEFAULT_LB_PORT = 8000

# Single client for load balancer endpoint
client: Optional[AsyncOpenAI] = None
lb_endpoint: str = ""


def get_gotoni_db_path() -> str:
    """Return path to the gotoni SQLite database."""
    if os.name == 'nt':
        config_dir = os.path.join(os.environ.get('LOCALAPPDATA', ''), 'gotoni')
    elif os.uname().sysname == 'Darwin':
        config_dir = os.path.join(os.path.expanduser('~'), 'Library', 'Application Support', 'gotoni')
    else:
        config_dir = os.path.join(os.environ.get('XDG_CONFIG_HOME', os.path.expanduser('~/.config')), 'gotoni')
    return os.path.join(config_dir, 'data.db')


def discover_cluster_instances(cluster_name: str) -> List[str]:
    """Discover instance IPs for a cluster from the gotoni SQLite database.

    Args:
        cluster_name: Name of the cluster (e.g., 'sglang-auto-cluster')

    Returns:
        List of IP addresses for instances in the cluster.

    Raises:
        RuntimeError: If cluster not found or database unavailable.
    """
    db_path = get_gotoni_db_path()
    if not os.path.exists(db_path):
        raise RuntimeError(f"Gotoni database not found at {db_path}. "
                           "Has the cluster been created?")

    conn = sqlite3.connect(db_path)
    try:
        cursor = conn.cursor()

        # Find the cluster by name
        cursor.execute("SELECT id FROM clusters WHERE name = ?", (cluster_name,))
        row = cursor.fetchone()
        if not row:
            # List available clusters to help the user
            cursor.execute("SELECT name FROM clusters")
            available = [r[0] for r in cursor.fetchall()]
            raise RuntimeError(
                f"Cluster '{cluster_name}' not found. "
                f"Available clusters: {available or 'none'}"
            )
        cluster_id = row[0]

        # Get instance IPs for this cluster
        cursor.execute("""
            SELECT DISTINCT i.ip_address, i.name, i.id
            FROM instances i
            JOIN cluster_instances ci ON i.id = ci.instance_id
            WHERE ci.cluster_id = ?
              AND i.ip_address IS NOT NULL
              AND i.ip_address != ''
        """, (cluster_id,))
        instances = cursor.fetchall()

        if not instances:
            raise RuntimeError(f"No instances with IPs found for cluster '{cluster_name}'")

        print(f"Discovered {len(instances)} instance(s) in cluster '{cluster_name}':")
        ips = []
        for ip, name, inst_id in instances:
            print(f"  - {name} ({inst_id[:16]}) @ {ip}")
            ips.append(ip)
        return ips
    finally:
        conn.close()


@dataclass
class LBMetricsSnapshot:
    """Snapshot of load balancer metrics at a point in time."""
    timestamp: float
    local: Dict[str, Any]
    peers: Dict[str, Dict[str, Any]]
    config: Dict[str, Any]


@dataclass
class BenchmarkMetrics:
    """Comprehensive benchmark metrics."""
    strategy: str
    lb_endpoint: str
    duration: int
    num_users: int

    # Pre-benchmark state
    pre_metrics: Optional[LBMetricsSnapshot] = None

    # During-benchmark samples
    periodic_metrics: List[LBMetricsSnapshot] = field(default_factory=list)

    # Post-benchmark state
    post_metrics: Optional[LBMetricsSnapshot] = None

    # Request-level metrics
    total_requests: int = 0
    total_errors: int = 0
    latencies: List[float] = field(default_factory=list)
    instance_distribution: Dict[str, int] = field(default_factory=dict)

    # Computed metrics (filled after benchmark)
    avg_latency: float = 0.0
    p50_latency: float = 0.0
    p95_latency: float = 0.0
    p99_latency: float = 0.0
    throughput: float = 0.0
    error_rate: float = 0.0
    actual_duration: float = 0.0


def init_client(endpoint: str, model: str):
    """Initialize single OpenAI client pointing to load balancer."""
    global client, lb_endpoint
    lb_endpoint = endpoint
    base_url = f"{endpoint}/v1"
    client = AsyncOpenAI(
        base_url=base_url,
        api_key='not-needed',  # SGLang doesn't require API key
    )
    print(f"Initialized client for load balancer: {base_url}")


async def fetch_lb_metrics(endpoint: str) -> Optional[LBMetricsSnapshot]:
    """Fetch metrics from the load balancer's /lb/metrics endpoint."""
    try:
        async with aiohttp.ClientSession() as session:
            async with session.get(f"{endpoint}/lb/metrics", timeout=aiohttp.ClientTimeout(total=5)) as resp:
                if resp.status == 200:
                    data = await resp.json()
                    return LBMetricsSnapshot(
                        timestamp=time.time(),
                        local=data.get('local', {}),
                        peers=data.get('peers', {}),
                        config=data.get('config', {}),
                    )
    except Exception as e:
        print(f"Warning: Failed to fetch LB metrics: {e}")
    return None


async def fetch_lb_health(endpoint: str) -> bool:
    """Check if load balancer is healthy."""
    try:
        async with aiohttp.ClientSession() as session:
            async with session.get(f"{endpoint}/lb/health", timeout=aiohttp.ClientTimeout(total=5)) as resp:
                return resp.status == 200
    except Exception:
        return False


async def periodic_metrics_poller(
    endpoint: str,
    interval: int,
    metrics_list: List[LBMetricsSnapshot],
    stop_event: asyncio.Event,
):
    """Periodically poll and store LB metrics during benchmark."""
    while not stop_event.is_set():
        snapshot = await fetch_lb_metrics(endpoint)
        if snapshot:
            metrics_list.append(snapshot)
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=interval)
        except asyncio.TimeoutError:
            pass  # Continue polling


async def periodic_request_stats_printer(
    stats: Dict[str, Any],
    tic: float,
    interval: int,
    stop_event: asyncio.Event,
):
    """Print live request metrics every `interval` seconds during benchmark."""
    prev_requests = 0
    prev_errors = 0
    prev_time = tic

    while not stop_event.is_set():
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=interval)
        except asyncio.TimeoutError:
            pass  # Continue printing

        if stop_event.is_set():
            break

        now = time.time()
        elapsed = now - tic
        dt = now - prev_time
        reqs = stats['requests']
        errs = stats['errors']
        delta_reqs = reqs - prev_requests
        delta_errs = errs - prev_errors
        interval_rps = delta_reqs / dt if dt > 0 else 0.0

        latencies = stats['latencies']
        if latencies:
            recent = latencies[-delta_reqs:] if delta_reqs > 0 else latencies[-1:]
            avg = statistics.mean(recent) if recent else 0.0
            sorted_all = sorted(latencies)
            n = len(sorted_all)
            p50 = sorted_all[n // 2]
            p95 = sorted_all[int(n * 0.95)] if n > 1 else sorted_all[-1]
        else:
            avg = p50 = p95 = 0.0

        dist_summary = ', '.join(
            f'{k}:{v}' for k, v in sorted(
                stats['instance_requests'].items(), key=lambda x: -x[1]
            )
        )

        print(f'\n--- [{elapsed:.0f}s] REQUEST METRICS ---')
        print(f'  Requests: {reqs} (+{delta_reqs})  Errors: {errs} (+{delta_errs})')
        print(f'  Interval throughput: {interval_rps:.2f} req/s')
        print(f'  Latency (recent avg): {avg:.3f}s  P50: {p50:.3f}s  P95: {p95:.3f}s')
        if dist_summary:
            print(f'  Distribution: {dist_summary}')
        print()

        prev_requests = reqs
        prev_errors = errs
        prev_time = now


async def call_chat_completion(
    messages: List[Dict[str, str]],
    model: str,
    temperature: float = 0.0,
    max_tokens: int = 512,
) -> Tuple[Optional[str], str]:
    """Call chat completion via load balancer.

    Returns:
        Tuple of (response_content, served_by_info)
    """
    try:
        response = await client.chat.completions.create(
            model=model,
            messages=messages,
            temperature=temperature,
            max_tokens=max_tokens,
        )
        # Try to extract which instance served the request
        served_by = "unknown"
        if hasattr(response, '_response') and response._response:
            headers = getattr(response._response, 'headers', {})
            served_by = headers.get('X-Served-By', 'local')
        return response.choices[0].message.content, served_by
    except Exception as e:
        print(f"Error calling load balancer: {e}")
        return None, "error"


def _load_dataset(start_index: int) -> List[Dict[str, Any]]:
    """Load and filter the WildChat dataset."""
    tic = time.time()
    split_slice = f'{start_index*100000}:{(start_index+1)*100000}'
    print(f"Loading dataset slice: train[{split_slice}]...")
    chunk_data = datasets.load_dataset(DATASET_NAME, split=f'train[{split_slice}]')

    multi_turn_data = []
    for d in chunk_data:
        # At least 2 full turns: user + assistant + user + assistant (len >= 4)
        if d['turn'] >= 2 and isinstance(d['conversation'], list) and len(d['conversation']) >= 20:
            if d.get('hashed_ip', 'unknown') == 'unknown':
                continue
            conv = {
                'turn': d['turn'],
                'timestamp': d['timestamp'].timestamp(),
                'conv': d['conversation'],
                'user': d['hashed_ip'],
                'state': d['state'],
                'country': d['country'],
            }
            multi_turn_data.append(conv)

    print(f'Loaded {len(multi_turn_data)} multi-turn conversations (took {time.time() - tic:.2f}s)')
    return multi_turn_data


async def _multi_turn_conv(
    uid: int,
    idx: int,
    duration: int,
    tic: float,
    conv: Dict[str, Any],
    model: str,
    stats: Dict[str, Any],
) -> None:
    """Execute a multi-turn conversation."""
    history = []

    for i, msg in enumerate(conv['conv']):
        elapsed = time.time() - tic
        remaining = duration - elapsed
        if remaining <= 0:
            break

        if i % 2 == 0:
            # User message
            assert msg['role'] == 'user'
            history.append({'role': 'user', 'content': msg['content']})
        else:
            # Assistant turn - make API call
            assert msg['role'] == 'assistant'
            st_this_round = time.time()

            result, served_by = await call_chat_completion(
                messages=history,
                model=model,
            )

            if result is None:
                stats['errors'] += 1
                return

            history.append({'role': 'assistant', 'content': result})

            latency = time.time() - st_this_round
            stats['requests'] += 1
            stats['total_latency'] += latency
            stats['latencies'].append(latency)

            # Track per-instance distribution
            stats['instance_requests'][served_by] = stats['instance_requests'].get(served_by, 0) + 1

            print(f'[{time.time() - tic:.2f}s] User {uid} conv {idx} turn {i//2+1} '
                  f'-> {served_by} ({latency:.2f}s)')


async def _user_task(
    duration: int,
    tic: float,
    uid: int,
    convs: List[Dict[str, Any]],
    model: str,
    stats: Dict[str, Any],
) -> None:
    """Run all conversations for a single user."""
    for i, conv in enumerate(convs):
        elapsed = time.time() - tic
        if elapsed >= duration:
            break

        try:
            await _multi_turn_conv(uid, i, duration, tic, conv, model, stats)
        except asyncio.CancelledError:
            break
        except Exception as e:
            print(f'User {uid}: Error in conversation {i}: {e}')
            stats['errors'] += 1


async def run_benchmark(
    endpoint: str,
    strategy: str,
    duration: int = 30,
    num_users: int = 20,
    start_index: int = 0,
    model: str = 'default',
    max_convs: int = 100,
    metrics_interval: int = 5,
) -> BenchmarkMetrics:
    """Run the benchmark with the specified parameters."""

    # Initialize metrics container
    benchmark_metrics = BenchmarkMetrics(
        strategy=strategy,
        lb_endpoint=endpoint,
        duration=duration,
        num_users=num_users,
    )

    # Check LB health before starting
    print(f"Checking load balancer health at {endpoint}...")
    if not await fetch_lb_health(endpoint):
        raise RuntimeError(f"Load balancer at {endpoint} is not healthy")
    print("Load balancer is healthy")

    # Initialize client
    init_client(endpoint, model)

    # Collect pre-benchmark metrics
    print("Collecting pre-benchmark metrics...")
    benchmark_metrics.pre_metrics = await fetch_lb_metrics(endpoint)
    if benchmark_metrics.pre_metrics:
        print(f"  Local: {benchmark_metrics.pre_metrics.local.get('total_reqs', 0)} total reqs, "
              f"GPU cache: {benchmark_metrics.pre_metrics.local.get('gpu_cache', 0):.2%}")
        print(f"  Peers: {len(benchmark_metrics.pre_metrics.peers)} connected")

    # Load dataset
    convs = _load_dataset(start_index)

    # Limit conversations for quick testing
    if max_convs and len(convs) > max_convs:
        convs = convs[:max_convs]
        print(f"Limited to {max_convs} conversations for testing")

    # Group conversations by user
    user_to_convs: Dict[str, List[Dict[str, Any]]] = collections.defaultdict(list)
    for conv in convs:
        user_to_convs[conv['user']].append(conv)

    sorted_users = sorted(user_to_convs.keys(), key=lambda u: len(user_to_convs[u]), reverse=True)

    # Distribute conversations across simulated users
    groups: Dict[int, List[Dict[str, Any]]] = collections.defaultdict(list)
    for user in sorted_users:
        min_group_idx = min(range(num_users), key=lambda idx: len(groups[idx]))
        groups[min_group_idx].extend(user_to_convs[user])

    print(f'Distributed {len(convs)} conversations across {num_users} users')
    print(f'Group sizes: {[len(g) for g in groups.values()]}')

    # Initialize stats
    stats = {
        'requests': 0,
        'errors': 0,
        'total_latency': 0.0,
        'latencies': [],
        'instance_requests': {},
    }

    # Start periodic metrics poller
    stop_event = asyncio.Event()
    poller_task = asyncio.create_task(
        periodic_metrics_poller(endpoint, metrics_interval, benchmark_metrics.periodic_metrics, stop_event)
    )

    # Run benchmark
    print(f'\n{"="*60}')
    print(f'Starting benchmark for {duration}s with {num_users} concurrent users')
    print(f'Strategy: {strategy}')
    print(f'Load Balancer: {endpoint}')
    print(f'{"="*60}\n')

    tic = time.time()

    # Start periodic request stats printer
    stats_printer_task = asyncio.create_task(
        periodic_request_stats_printer(stats, tic, metrics_interval, stop_event)
    )

    tasks = []
    for uid, user_convs in groups.items():
        if user_convs:
            user_convs.sort(key=lambda c: c['timestamp'])
            tasks.append(_user_task(duration, tic, uid, user_convs, model, stats))

    await asyncio.gather(*tasks, return_exceptions=True)

    total_time = time.time() - tic

    # Stop metrics poller and stats printer
    stop_event.set()
    await poller_task
    await stats_printer_task

    # Collect post-benchmark metrics
    print("\nCollecting post-benchmark metrics...")
    benchmark_metrics.post_metrics = await fetch_lb_metrics(endpoint)

    # Populate benchmark metrics
    benchmark_metrics.total_requests = stats['requests']
    benchmark_metrics.total_errors = stats['errors']
    benchmark_metrics.latencies = stats['latencies']
    benchmark_metrics.instance_distribution = stats['instance_requests']
    benchmark_metrics.actual_duration = total_time

    if stats['requests'] > 0:
        latencies_sorted = sorted(stats['latencies'])
        n = len(latencies_sorted)
        benchmark_metrics.avg_latency = statistics.mean(latencies_sorted)
        benchmark_metrics.p50_latency = latencies_sorted[n // 2]
        benchmark_metrics.p95_latency = latencies_sorted[int(n * 0.95)] if n > 1 else latencies_sorted[-1]
        benchmark_metrics.p99_latency = latencies_sorted[int(n * 0.99)] if n > 1 else latencies_sorted[-1]
        benchmark_metrics.throughput = stats['requests'] / total_time
        benchmark_metrics.error_rate = stats['errors'] / (stats['requests'] + stats['errors'])

    return benchmark_metrics


def print_benchmark_results(metrics: BenchmarkMetrics):
    """Print comprehensive benchmark results."""
    print(f'\n{"="*60}')
    print('BENCHMARK RESULTS')
    print(f'{"="*60}')
    print(f'Strategy: {metrics.strategy}')
    print(f'LB Endpoint: {metrics.lb_endpoint}')
    print(f'Duration: {metrics.actual_duration:.2f}s (target: {metrics.duration}s)')
    print(f'Concurrent Users: {metrics.num_users}')
    print()

    print('REQUEST METRICS:')
    print(f'  Total requests: {metrics.total_requests}')
    print(f'  Total errors: {metrics.total_errors}')
    print(f'  Error rate: {metrics.error_rate:.2%}')
    print(f'  Throughput: {metrics.throughput:.2f} req/s')
    print()

    print('LATENCY METRICS:')
    print(f'  Average: {metrics.avg_latency:.3f}s')
    print(f'  P50: {metrics.p50_latency:.3f}s')
    print(f'  P95: {metrics.p95_latency:.3f}s')
    print(f'  P99: {metrics.p99_latency:.3f}s')
    print()

    print('INSTANCE DISTRIBUTION:')
    total = sum(metrics.instance_distribution.values())
    for instance_id, count in sorted(metrics.instance_distribution.items(), key=lambda x: -x[1]):
        pct = (count / total * 100) if total > 0 else 0
        print(f'  {instance_id}: {count} requests ({pct:.1f}%)')
    print()

    # Pre/Post comparison
    if metrics.pre_metrics and metrics.post_metrics:
        print('LOAD BALANCER STATE COMPARISON:')
        pre_total = metrics.pre_metrics.local.get('total_reqs', 0)
        post_total = metrics.post_metrics.local.get('total_reqs', 0)
        pre_cache = metrics.pre_metrics.local.get('gpu_cache', 0)
        post_cache = metrics.post_metrics.local.get('gpu_cache', 0)
        print(f'  Pre-benchmark total reqs (local): {pre_total}')
        print(f'  Post-benchmark total reqs (local): {post_total}')
        print(f'  Pre-benchmark GPU cache: {pre_cache:.2%}')
        print(f'  Post-benchmark GPU cache: {post_cache:.2%}')

    if metrics.periodic_metrics:
        print(f'\nMETRICS SAMPLES: {len(metrics.periodic_metrics)} snapshots collected')


def export_benchmark_results(metrics: BenchmarkMetrics, output_file: str):
    """Export benchmark results to JSON file."""
    data = asdict(metrics)

    # Convert LBMetricsSnapshot objects to dicts
    if metrics.pre_metrics:
        data['pre_metrics'] = asdict(metrics.pre_metrics)
    if metrics.post_metrics:
        data['post_metrics'] = asdict(metrics.post_metrics)
    data['periodic_metrics'] = [asdict(m) for m in metrics.periodic_metrics]

    with open(output_file, 'w') as f:
        json.dump(data, f, indent=2, default=str)
    print(f'\nResults exported to {output_file}')


async def find_healthy_endpoint(instance_ips: List[str], port: int) -> str:
    """Find the first healthy load balancer endpoint from a list of instance IPs.

    Checks all instances in parallel and returns the first healthy one.
    """
    async def check_health(ip: str) -> Optional[str]:
        endpoint = f"http://{ip}:{port}"
        if await fetch_lb_health(endpoint):
            return endpoint
        return None

    tasks = [check_health(ip) for ip in instance_ips]
    results = await asyncio.gather(*tasks)
    healthy = [r for r in results if r is not None]

    if not healthy:
        tried = [f"http://{ip}:{port}" for ip in instance_ips]
        raise RuntimeError(
            f"No healthy load balancer found. Tried: {tried}\n"
            "Are the load balancers running? Check with: gotoni lb-remote-status --all"
        )

    print(f"Found {len(healthy)} healthy load balancer(s), using: {healthy[0]}")
    return healthy[0]


def main():
    parser = argparse.ArgumentParser(description='WildChat benchmark with gotoni load balancing')

    # Cluster discovery (auto-discovers IPs from gotoni database)
    parser.add_argument('--cluster-name', type=str, default=DEFAULT_CLUSTER_NAME,
                        help=f'Cluster name to discover instances from (default: {DEFAULT_CLUSTER_NAME})')
    parser.add_argument('--lb-endpoint', type=str, default=None,
                        help='Override: use this LB endpoint directly instead of cluster discovery')
    parser.add_argument('--lb-port', type=int, default=DEFAULT_LB_PORT,
                        help=f'Load balancer port on each instance (default: {DEFAULT_LB_PORT})')

    # Strategy selection (for documentation/output - LB must be pre-configured)
    parser.add_argument('--strategy', type=str, default='least-loaded',
                        choices=['least-loaded', 'prefix-tree', 'gorgo'],
                        help='Load balancing policy label (default: least-loaded)')

    # Benchmark parameters
    parser.add_argument('--duration', type=int, default=30,
                        help='Benchmark duration in seconds')
    parser.add_argument('--num-users', type=int, default=20,
                        help='Number of concurrent users')
    parser.add_argument('--start-index', type=int, default=0,
                        help='Dataset chunk index (0-9)')
    parser.add_argument('--model', type=str, default='default',
                        help='Model name to use')
    parser.add_argument('--max-convs', type=int, default=50,
                        help='Max conversations for testing (0 for unlimited)')

    # Metrics collection
    parser.add_argument('--metrics-interval', type=int, default=5,
                        help='Seconds between periodic metrics polls')
    parser.add_argument('--output-file', type=str, default=None,
                        help='Optional JSON file for results export')

    args = parser.parse_args()

    # Resolve the LB endpoint
    if args.lb_endpoint:
        # User provided an explicit endpoint - use it directly
        endpoint = args.lb_endpoint
        print(f"Using provided endpoint: {endpoint}")
    else:
        # Auto-discover from cluster database
        print(f"Discovering instances from cluster '{args.cluster_name}'...")
        instance_ips = discover_cluster_instances(args.cluster_name)
        endpoint = asyncio.run(find_healthy_endpoint(instance_ips, args.lb_port))

    # Run benchmark
    metrics = asyncio.run(run_benchmark(
        endpoint=endpoint,
        strategy=args.strategy,
        duration=args.duration,
        num_users=args.num_users,
        start_index=args.start_index,
        model=args.model,
        max_convs=args.max_convs if args.max_convs > 0 else None,
        metrics_interval=args.metrics_interval,
    ))

    # Print results
    print_benchmark_results(metrics)

    # Export if requested
    if args.output_file:
        export_benchmark_results(metrics, args.output_file)


if __name__ == '__main__':
    main()
