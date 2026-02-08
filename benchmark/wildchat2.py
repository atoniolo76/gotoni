"""WildChat-1M geo-distributed benchmark for GORGO policy testing.

Routes requests to geographically nearest cluster node based on user location.
Uses Euclidean distance on lat/lon coordinates for nearest-neighbor selection.
"""

import argparse
import asyncio
import collections
import dataclasses
import json
import math
import time
from functools import lru_cache
from typing import Any, Dict, List, Optional, Tuple

import aiohttp
import datasets
import openai
from openai.types.chat import ChatCompletionStreamOptionsParam

# =============================================================================
# Constants
# =============================================================================

DATASET_NAME = 'allenai/WildChat-1M'

# Cluster node definitions with coordinates
# Format: (name, ip, port, latitude, longitude)
CLUSTER_NODES = [
    ("gpu-2", "151.145.83.16", 8000, 31.0461, 34.8516),              # Israel (Tel Aviv area)
    ("gotoni-germany", "130.61.109.93", 8000, 50.1109, 8.6821),      # Frankfurt, Germany
    ("west_coast", "192.9.140.201", 8000, 37.7749, -122.4194),       # San Francisco, CA, USA
]

# Country/State to approximate coordinates (lat, lon)
# Major countries and US states
LOCATION_COORDS: Dict[str, Tuple[float, float]] = {
    # North America
    "United States": (39.8283, -98.5795),
    "Canada": (56.1304, -106.3468),
    "Mexico": (23.6345, -102.5528),
    # US States (East)
    "Virginia": (37.4316, -78.6569),
    "North Carolina": (35.7596, -79.0193),
    "South Carolina": (33.8361, -81.1637),
    "Georgia": (32.1656, -82.9001),
    "Florida": (27.6648, -81.5158),
    "Tennessee": (35.5175, -86.5804),
    "Kentucky": (37.8393, -84.2700),
    "West Virginia": (38.5976, -80.4549),
    "Maryland": (39.0458, -76.6413),
    "Delaware": (38.9108, -75.5277),
    "Pennsylvania": (41.2033, -77.1945),
    "New York": (40.7128, -74.0060),
    "New Jersey": (40.0583, -74.4057),
    "Connecticut": (41.6032, -73.0877),
    "Massachusetts": (42.4072, -71.3824),
    "Ohio": (40.4173, -82.9071),
    "Indiana": (40.2672, -86.1349),
    "Illinois": (40.6331, -89.3985),
    "Michigan": (44.3148, -85.6024),
    "Wisconsin": (43.7844, -88.7879),
    "Minnesota": (46.7296, -94.6859),
    "Iowa": (41.8780, -93.0977),
    "Missouri": (37.9643, -91.8318),
    "Arkansas": (35.2010, -91.8318),
    # US States (West)
    "California": (36.7783, -119.4179),
    "Washington": (47.7511, -120.7401),
    "Oregon": (43.8041, -120.5542),
    "Nevada": (38.8026, -116.4194),
    "Arizona": (34.0489, -111.0937),
    "Colorado": (39.5501, -105.7821),
    "Texas": (31.9686, -99.9018),
    # Europe
    "United Kingdom": (55.3781, -3.4360),
    "Ireland": (53.1424, -7.6921),
    "France": (46.2276, 2.2137),
    "Germany": (51.1657, 10.4515),
    "Spain": (40.4637, -3.7492),
    "Italy": (41.8719, 12.5674),
    "Portugal": (39.3999, -8.2245),
    "Netherlands": (52.1326, 5.2913),
    "Belgium": (50.5039, 4.4699),
    "Switzerland": (46.8182, 8.2275),
    "Austria": (47.5162, 14.5501),
    "Poland": (51.9194, 19.1451),
    "Czech Republic": (49.8175, 15.4730),
    "Hungary": (47.1625, 19.5033),
    "Sweden": (60.1282, 18.6435),
    "Norway": (60.4720, 8.4689),
    "Finland": (61.9241, 25.7482),
    "Denmark": (56.2639, 9.5018),
    "Greece": (39.0742, 21.8243),
    "Romania": (45.9432, 24.9668),
    "Bulgaria": (42.7339, 25.4858),
    "Croatia": (45.1000, 15.2000),
    "Slovakia": (48.6690, 19.6990),
    "Slovenia": (46.1512, 14.9955),
    "Estonia": (58.5953, 25.0136),
    "Latvia": (56.8796, 24.6032),
    "Lithuania": (55.1694, 23.8813),
    "Ukraine": (48.3794, 31.1656),
    "Russia": (61.5240, 105.3188),
    # Middle East
    "Israel": (31.0461, 34.8516),
    "Turkey": (38.9637, 35.2433),
    "Saudi Arabia": (23.8859, 45.0792),
    "United Arab Emirates": (23.4241, 53.8478),
    "Iran": (32.4279, 53.6880),
    "Egypt": (26.8206, 30.8025),
    # Asia
    "Japan": (36.2048, 138.2529),
    "South Korea": (35.9078, 127.7669),
    "China": (35.8617, 104.1954),
    "India": (20.5937, 78.9629),
    "Pakistan": (30.3753, 69.3451),
    "Bangladesh": (23.6850, 90.3563),
    "Vietnam": (14.0583, 108.2772),
    "Thailand": (15.8700, 100.9925),
    "Indonesia": (-0.7893, 113.9213),
    "Malaysia": (4.2105, 101.9758),
    "Philippines": (12.8797, 121.7740),
    "Singapore": (1.3521, 103.8198),
    "Taiwan": (23.6978, 120.9605),
    "Hong Kong": (22.3193, 114.1694),
    # Oceania
    "Australia": (-25.2744, 133.7751),
    "New Zealand": (-40.9006, 174.8860),
    # South America
    "Brazil": (-14.2350, -51.9253),
    "Argentina": (-38.4161, -63.6167),
    "Chile": (-35.6751, -71.5430),
    "Colombia": (4.5709, -74.2973),
    "Peru": (-9.1900, -75.0152),
    # Africa
    "South Africa": (-30.5595, 22.9375),
    "Nigeria": (9.0820, 8.6753),
    "Kenya": (-0.0236, 37.9062),
    "Morocco": (31.7917, -7.0926),
}


# =============================================================================
# Geo Utilities
# =============================================================================

def haversine_distance(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    """Calculate great-circle distance between two points in km."""
    R = 6371  # Earth radius in km
    
    lat1_rad = math.radians(lat1)
    lat2_rad = math.radians(lat2)
    dlat = math.radians(lat2 - lat1)
    dlon = math.radians(lon2 - lon1)
    
    a = math.sin(dlat/2)**2 + math.cos(lat1_rad) * math.cos(lat2_rad) * math.sin(dlon/2)**2
    c = 2 * math.atan2(math.sqrt(a), math.sqrt(1-a))
    
    return R * c


@lru_cache(maxsize=10000)
def get_location_coords(country: str, state: Optional[str] = None) -> Tuple[float, float]:
    """Get coordinates for a location, with caching."""
    # Try state first (for US)
    if state and state in LOCATION_COORDS:
        return LOCATION_COORDS[state]
    
    # Try country
    if country in LOCATION_COORDS:
        return LOCATION_COORDS[country]
    
    # Default to center of world (will route to nearest node anyway)
    return (0.0, 0.0)


def find_nearest_node(lat: float, lon: float) -> Tuple[str, str, int]:
    """Find the nearest cluster node to given coordinates.
    
    Returns: (node_name, ip, port)
    """
    min_dist = float('inf')
    nearest = CLUSTER_NODES[0]
    
    for node in CLUSTER_NODES:
        name, ip, port, node_lat, node_lon = node
        dist = haversine_distance(lat, lon, node_lat, node_lon)
        if dist < min_dist:
            min_dist = dist
            nearest = node
    
    return nearest[0], nearest[1], nearest[2]


# =============================================================================
# Metrics
# =============================================================================

@dataclasses.dataclass
class Metric:
    """Metric for each request."""
    uid: str
    node: str = ""
    start: Optional[float] = None
    end: Optional[float] = None
    ttft: Optional[float] = None
    e2e_latency: Optional[float] = None
    failed: Optional[str] = None
    input_tokens: Optional[int] = None
    output_tokens: int = 0
    user_lat: float = 0.0
    user_lon: float = 0.0
    distance_km: float = 0.0


global_metrics: List[Metric] = []
metrics_lock: Optional[asyncio.Lock] = None

# Per-node OpenAI clients
node_clients: Dict[str, openai.AsyncOpenAI] = {}
node_models: Dict[str, str] = {}


# =============================================================================
# Client Initialization
# =============================================================================

async def init_node_clients() -> None:
    """Initialize OpenAI clients for all cluster nodes."""
    global metrics_lock
    metrics_lock = asyncio.Lock()
    
    async with aiohttp.ClientSession() as session:
        for name, ip, port, _, _ in CLUSTER_NODES:
            url = f"http://{ip}:{port}"
            try:
                async with session.get(f'{url}/v1/models', timeout=aiohttp.ClientTimeout(total=10)) as resp:
                    data = await resp.json()
                    model = data['data'][0]['id']
                    node_models[name] = model
                    node_clients[name] = openai.AsyncOpenAI(
                        base_url=f'{url}/v1',
                        api_key='placeholder',
                        max_retries=0
                    )
                    print(f"✓ Connected to {name} ({ip}:{port}) - model: {model}")
            except Exception as e:
                print(f"✗ Failed to connect to {name} ({ip}:{port}): {e}")


# =============================================================================
# Request Execution
# =============================================================================

async def send_request(
    node_name: str,
    messages: List[Dict[str, str]],
    uid: str,
    user_lat: float,
    user_lon: float,
    distance_km: float,
    max_tokens: int = 512,
    timeout: float = 60.0,
) -> Metric:
    """Send a single request to a specific node."""
    metric = Metric(
        uid=uid,
        node=node_name,
        user_lat=user_lat,
        user_lon=user_lon,
        distance_km=distance_km,
    )
    
    if node_name not in node_clients:
        metric.failed = f"Node {node_name} not available"
        return metric
    
    client = node_clients[node_name]
    model = node_models[node_name]
    
    try:
        st = time.time()
        metric.start = st
        
        res = await asyncio.wait_for(
            client.chat.completions.create(
                model=model,
                messages=messages,  # type: ignore
                temperature=0.0,
                max_tokens=max_tokens,
                stream=True,
                stream_options=ChatCompletionStreamOptionsParam(include_usage=True),
                timeout=timeout,
            ),
            timeout=timeout,
        )
        
        output = ''
        output_token_count = 0
        async for chunk in res:
            t_now = time.time()
            
            if chunk.choices and len(chunk.choices) > 0:
                choice = chunk.choices[0]
            
                if metric.ttft is None and choice.delta and choice.delta.content:
                    metric.ttft = t_now - st
            
                if choice.delta and choice.delta.content:
                    output += choice.delta.content
                    output_token_count += 1  # Approximate: count chunks as tokens
                
                if choice.finish_reason:
                    metric.e2e_latency = t_now - st
                    metric.end = t_now
            
            # Usage comes in final chunk
            if chunk.usage:
                metric.input_tokens = chunk.usage.prompt_tokens
                metric.output_tokens = chunk.usage.completion_tokens
        
        # Fallback token count if usage wasn't provided
        if metric.output_tokens == 0:
            metric.output_tokens = output_token_count
        
    except asyncio.TimeoutError:
        metric.failed = "timeout"
    except Exception as e:
        metric.failed = str(e)[:100]
    
    return metric


# =============================================================================
# Dataset Loading
# =============================================================================

def load_dataset_with_coords(start_index: int, max_convs: int = 1000) -> List[Dict[str, Any]]:
    """Load WildChat dataset and add coordinates for each conversation."""
    tic = time.time()
    
    # Load a slice of the dataset
    split_slice = f'{start_index}:{start_index + max_convs}'
    print(f'Loading dataset slice: train[{split_slice}]...')
    chunk_data = datasets.load_dataset(DATASET_NAME, split=f'train[{split_slice}]')
    
    conversations = []
    node_counts = collections.defaultdict(int)
    
    for d in chunk_data:
        # Need at least 2 turns
        if d['turn'] < 2 or not isinstance(d['conversation'], list) or len(d['conversation']) < 4:
            continue

        country = d.get('country', 'Unknown')
        state = d.get('state', None)

        # Get user coordinates
        lat, lon = get_location_coords(country, state)

        # Find nearest node
        node_name, node_ip, node_port = find_nearest_node(lat, lon)

        # Calculate distance
        node_info = next((n for n in CLUSTER_NODES if n[0] == node_name), None)
        if node_info:
            distance = haversine_distance(lat, lon, node_info[3], node_info[4])
        else:
            distance = 0

        conv = {
                'turn': d['turn'],
                'conv': d['conversation'],
            'country': country,
            'state': state,
            'lat': lat,
            'lon': lon,
            'nearest_node': node_name,
            'node_ip': node_ip,
            'node_port': node_port,
            'distance_km': distance,
        }
        conversations.append(conv)
        node_counts[node_name] += 1
    
    print(f'Loaded {len(conversations)} conversations (took {time.time() - tic:.2f}s)')
    print(f'Distribution by nearest node:')
    for node, count in sorted(node_counts.items()):
        print(f'  {node}: {count} ({100*count/len(conversations):.1f}%)')
    
    return conversations


# =============================================================================
# Benchmark Runner
# =============================================================================

async def run_constant_rate_benchmark(
    conversations: List[Dict[str, Any]],
    requests_per_second: float,
    duration: int,
    max_tokens: int = 256,
) -> None:
    """Run benchmark at constant request rate, routing to nearest node."""
    
    print(f'\n{"="*60}')
    print(f'Starting geo-distributed benchmark')
    print(f'  Rate: {requests_per_second} req/s')
    print(f'  Duration: {duration}s')
    print(f'  Max tokens: {max_tokens}')
    print(f'{"="*60}\n')
    
    interval = 1.0 / requests_per_second
    start_time = time.time()
    request_id = 0
    pending_tasks: List[asyncio.Task] = []
    
    conv_idx = 0
    
    while time.time() - start_time < duration:
        # Get next conversation (cycle through)
        conv = conversations[conv_idx % len(conversations)]
        conv_idx += 1
        
        # Build messages (first user turn only for simplicity)
        messages = []
        for i, msg in enumerate(conv['conv'][:2]):  # Just first exchange
            messages.append({'role': msg['role'], 'content': msg['content']})
            if msg['role'] == 'assistant':
                break

        if not messages or messages[-1]['role'] != 'user':
            # Need to end with user message
            messages = [{'role': 'user', 'content': conv['conv'][0]['content']}]
        
        # Create request task
        uid = f"req-{request_id}"
        task = asyncio.create_task(
            send_request(
                node_name=conv['nearest_node'],
                messages=messages,
                uid=uid,
                user_lat=conv['lat'],
                user_lon=conv['lon'],
                distance_km=conv['distance_km'],
                max_tokens=max_tokens,
            )
        )
        pending_tasks.append(task)
        request_id += 1
        
        # Log progress periodically
        if request_id % 10 == 0:
            elapsed = time.time() - start_time
            actual_rate = request_id / elapsed
            print(f'[{elapsed:.1f}s] Sent {request_id} requests ({actual_rate:.2f} req/s actual)')
        
        # Wait for next interval
        next_send_time = start_time + request_id * interval
        sleep_time = next_send_time - time.time()
        if sleep_time > 0:
            await asyncio.sleep(sleep_time)
    
    # Wait for remaining tasks
    print(f'\nWaiting for {len(pending_tasks)} pending requests...')
    results = await asyncio.gather(*pending_tasks, return_exceptions=True)
    
    # Collect metrics
    for result in results:
        if isinstance(result, Metric):
            global_metrics.append(result)
        elif isinstance(result, Exception):
            m = Metric(uid="error", failed=str(result))
            global_metrics.append(m)


# =============================================================================
# Results Summary
# =============================================================================

def print_results() -> Dict[str, Any]:
    """Print benchmark results."""
    if not global_metrics:
        print('No metrics collected.')
        return {}
    
    successful = [m for m in global_metrics if m.failed is None]
    failed = [m for m in global_metrics if m.failed is not None]
    
    print(f'\n{"="*60}')
    print('BENCHMARK RESULTS')
    print(f'{"="*60}')
    print(f'Total requests: {len(global_metrics)}')
    print(f'Successful: {len(successful)}')
    print(f'Failed: {len(failed)}')
    
    # Per-node breakdown
    print(f'\nPer-node breakdown:')
    node_metrics: Dict[str, List[Metric]] = collections.defaultdict(list)
    for m in global_metrics:
        node_metrics[m.node].append(m)
    
    for node, metrics in sorted(node_metrics.items()):
        node_success = [m for m in metrics if m.failed is None]
        node_ttfts = [m.ttft for m in node_success if m.ttft is not None]
        node_e2e = [m.e2e_latency for m in node_success if m.e2e_latency is not None]
        
        print(f'\n  {node}:')
        print(f'    Requests: {len(metrics)} ({len(node_success)} successful)')
        if node_ttfts:
            print(f'    TTFT: mean={sum(node_ttfts)/len(node_ttfts)*1000:.1f}ms, '
                  f'p50={sorted(node_ttfts)[len(node_ttfts)//2]*1000:.1f}ms')
        if node_e2e:
            print(f'    E2E:  mean={sum(node_e2e)/len(node_e2e):.2f}s, '
                  f'p50={sorted(node_e2e)[len(node_e2e)//2]:.2f}s')
    
    # Overall stats
    if successful:
        ttfts = [m.ttft for m in successful if m.ttft is not None]
        e2e_latencies = [m.e2e_latency for m in successful if m.e2e_latency is not None]
        distances = [m.distance_km for m in successful if m.distance_km > 0]
        
        print(f'\nOverall Statistics:')
        if ttfts:
            sorted_ttfts = sorted(ttfts)
            print(f'  TTFT:')
            print(f'    Mean: {sum(ttfts)/len(ttfts)*1000:.1f}ms')
            print(f'    P50:  {sorted_ttfts[len(sorted_ttfts)//2]*1000:.1f}ms')
            print(f'    P95:  {sorted_ttfts[int(len(sorted_ttfts)*0.95)]*1000:.1f}ms')
            print(f'    P99:  {sorted_ttfts[int(len(sorted_ttfts)*0.99)]*1000:.1f}ms')
        
        if e2e_latencies:
            sorted_e2e = sorted(e2e_latencies)
            print(f'  E2E Latency:')
            print(f'    Mean: {sum(e2e_latencies)/len(e2e_latencies):.2f}s')
            print(f'    P50:  {sorted_e2e[len(sorted_e2e)//2]:.2f}s')
            print(f'    P95:  {sorted_e2e[int(len(sorted_e2e)*0.95)]:.2f}s')
        
        if distances:
            print(f'  User-to-Node Distance:')
            print(f'    Mean: {sum(distances)/len(distances):.0f} km')
            print(f'    Max:  {max(distances):.0f} km')
        
        # Throughput
            start_times = [m.start for m in successful if m.start is not None]
            end_times = [m.end for m in successful if m.end is not None]
            if start_times and end_times:
                total_duration = max(end_times) - min(start_times)
            total_output_tokens = sum(m.output_tokens for m in successful)
            print(f'\n  Throughput:')
            print(f'    Requests/sec: {len(successful) / total_duration:.2f}')
            print(f'    Tokens/sec:   {total_output_tokens / total_duration:.1f}')
    
    if failed:
        print(f'\nFailures:')
        failure_counts: Dict[str, int] = collections.defaultdict(int)
        for m in failed:
            failure_counts[m.failed or 'unknown'] += 1
        for reason, count in sorted(failure_counts.items(), key=lambda x: -x[1])[:5]:
            print(f'  {reason[:50]}: {count}')
    
    return {
        'total': len(global_metrics),
        'successful': len(successful),
        'failed': len(failed),
    }


# =============================================================================
# Main
# =============================================================================

async def main_async(args: argparse.Namespace) -> None:
    """Async main entry point."""
    # Initialize clients
    print('Initializing cluster node connections...')
    await init_node_clients()
    
    if not node_clients:
        print('ERROR: No cluster nodes available!')
        return
    
    # Load dataset
    conversations = load_dataset_with_coords(
        start_index=args.start_index,
        max_convs=args.max_convs,
    )
    
    if not conversations:
        print('ERROR: No conversations loaded!')
        return
    
    # Run benchmark
    await run_constant_rate_benchmark(
        conversations=conversations,
        requests_per_second=args.rate,
        duration=args.duration,
        max_tokens=args.max_tokens,
    )
    
    # Print results
    print_results()
    
    # Save metrics to file
    if args.output:
        with open(args.output, 'w') as f:
            json.dump([dataclasses.asdict(m) for m in global_metrics], f, indent=2)
        print(f'\nMetrics saved to {args.output}')


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description='WildChat geo-distributed benchmark for GORGO policy testing'
    )
    parser.add_argument('--duration', type=int, default=60,
                        help='Benchmark duration in seconds (default: 60)')
    parser.add_argument('--rate', type=float, default=1.0,
                        help='Requests per second (default: 1.0)')
    parser.add_argument('--max-convs', type=int, default=1000,
                        help='Max conversations to load from dataset (default: 1000)')
    parser.add_argument('--start-index', type=int, default=0,
                        help='Dataset start index (default: 0)')
    parser.add_argument('--max-tokens', type=int, default=256,
                        help='Max tokens per response (default: 256)')
    parser.add_argument('--output', type=str, default=None,
                        help='Output file for metrics JSON')
    
    args = parser.parse_args()
    
    print(f'''
╔══════════════════════════════════════════════════════════════╗
║  WildChat Geo-Distributed Benchmark (GORGO Policy Testing)  ║
╚══════════════════════════════════════════════════════════════╝

Cluster Nodes:
''')
    for name, ip, port, lat, lon in CLUSTER_NODES:
        print(f'  • {name}: {ip}:{port} ({lat:.2f}, {lon:.2f})')
    print()
    
    asyncio.run(main_async(args))


if __name__ == '__main__':
    main()
