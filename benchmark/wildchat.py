"""WildChat-1M multi-turn conversation workload with synthetic timestamps.

Adapted to use gotoni cluster endpoints with geographic routing.
Includes TTFT (Time to First Token) and ITL (Inter-Token Latency) measurements.
"""

import argparse
import asyncio
import collections
import json
import os
import time
from dataclasses import dataclass, asdict
from datetime import datetime
from typing import Any, Awaitable, Dict, List, Optional

import datasets
from openai import AsyncOpenAI


@dataclass
class RequestMetrics:
    """Detailed metrics for a single request."""
    timestamp: float
    user_id: int
    conv_idx: int
    turn: int
    region: str
    country: str
    state: str
    
    # Latency breakdown
    ttft: float  # Time to First Token
    total_latency: float  # End-to-end latency
    itl_avg: float  # Average Inter-Token Latency
    itl_p50: float  # P50 Inter-Token Latency
    itl_p99: float  # P99 Inter-Token Latency
    
    # Token counts
    num_tokens: int
    input_tokens: int  # Approximate from message length
    
    # Status
    success: bool
    error: Optional[str] = None


async def flush_kv_cache(ip: str, port: int = 8080, timeout: float = 10.0) -> bool:
    """Flush the KV cache (RadixAttention cache) on an SGLang server.
    
    This is essential for accurate TTFT benchmarking as it removes any prefix cache hits.
    
    Args:
        ip: Server IP address
        port: SGLang server port (default 8080, the backend server not the LB)
        timeout: Request timeout in seconds
    
    Returns:
        True if flush succeeded, False otherwise
    """
    import aiohttp
    
    flush_url = f"http://{ip}:{port}/flush_cache"
    
    try:
        async with aiohttp.ClientSession() as session:
            async with session.post(flush_url, timeout=aiohttp.ClientTimeout(total=timeout)) as resp:
                if resp.status == 200:
                    return True
                else:
                    print(f"  Warning: Flush cache returned status {resp.status} for {ip}")
                    return False
    except Exception as e:
        print(f"  Warning: Failed to flush cache on {ip}: {e}")
        return False


async def flush_all_kv_caches(backend_port: int = 8080) -> Dict[str, bool]:
    """Flush KV caches on all SGLang backend servers.
    
    This ensures accurate TTFT measurements by clearing any prefix cache hits.
    
    Args:
        backend_port: Port where SGLang backend is running (default 8080)
    
    Returns:
        Dictionary mapping region to flush success status
    """
    print("\n" + "="*60)
    print("FLUSHING KV CACHES ON ALL ENDPOINTS")
    print("="*60)
    print("This ensures accurate TTFT measurements (no prefix cache hits)")
    
    # Create tasks for parallel execution
    regions = list(ENDPOINTS.keys())
    tasks = [flush_kv_cache(ENDPOINTS[region]['ip'], backend_port) for region in regions]
    
    # Execute all flushes in parallel
    flush_results = await asyncio.gather(*tasks, return_exceptions=True)
    
    # Process results
    results = {}
    for region, result in zip(regions, flush_results):
        if isinstance(result, Exception):
            success = False
            print(f"  ❌ {region} ({ENDPOINTS[region]['ip']}:{backend_port}) - {result}")
        else:
            success = result
            status = "✅" if success else "❌"
            print(f"  {status} {region} ({ENDPOINTS[region]['ip']}:{backend_port})")
        results[region] = success
    
    success_count = sum(1 for s in results.values() if s)
    print(f"\nFlushed {success_count}/{len(results)} endpoints")
    
    return results

# Import helper functions from wildchat2.py
# Note: In a real implementation, these would be in a shared utilities module
def get_location_coords(country: str, state: str = None):
    """Get coordinates for a location - simplified version."""
    # This is a placeholder - in practice you'd use the full implementation from wildchat2.py
    # For now, return approximate coordinates for major locations
    coords_map = {
        'United States': (39.8283, -98.5795),
        'Germany': (51.1657, 10.4515),
        'Israel': (31.0461, 34.8516),
        'United Kingdom': (55.3781, -3.4360),
        'France': (46.2276, 2.2137),
        'China': (35.8617, 104.1954),
        'Japan': (36.2048, 138.2529),
        'South Korea': (35.9078, 127.7669),
        'India': (20.5937, 78.9629),
        'Australia': (-25.2744, 133.7751),
        'Canada': (56.1304, -106.3468),
        'Russia': (61.5240, 105.3188),
        'Brazil': (-14.2350, -51.9253),
        'South Africa': (-30.5595, 22.9375),
    }

    if state and state in coords_map:
        return coords_map[state]
    if country and country in coords_map:
        return coords_map[country]

    # Default to center of world
    return (0.0, 0.0)

DATASET_NAME = 'allenai/WildChat-1M'

# Cluster endpoints - all three nodes from gotoni cluster
ENDPOINTS = {
    'west_coast': {
        'name': 'west_coast',
        'ip': '192.9.140.201',
        'base_url': 'http://192.9.140.201:8000/v1',
    },
    'germany': {
        'name': 'gotoni-germany',
        'ip': '130.61.109.93',
        'base_url': 'http://130.61.109.93:8000/v1',
    },
    'israel': {
        'name': 'gpu-2',
        'ip': '151.145.83.16',
        'base_url': 'http://151.145.83.16:8000/v1',
    },
}

# Geographic routing table - assign regions to closest cluster node
# Based on geographic proximity to: California (west_coast), Germany (germany), Israel (israel)

# West Coast node (California) - Americas
WEST_COAST_STATES = {
    'California', 'Oregon', 'Washington', 'Nevada', 'Arizona', 'Utah',
    'Colorado', 'New Mexico', 'Idaho', 'Montana', 'Wyoming', 'Alaska', 'Hawaii',
    'Texas', 'Oklahoma', 'Kansas', 'Nebraska', 'South Dakota', 'North Dakota',
    'Minnesota', 'Iowa', 'Missouri', 'Arkansas', 'Louisiana', 'Mississippi',
    'Alabama', 'Georgia', 'Florida', 'South Carolina', 'North Carolina',
    'Tennessee', 'Kentucky', 'West Virginia', 'Virginia', 'Maryland',
    'Delaware', 'New Jersey', 'Pennsylvania', 'Ohio', 'Michigan', 'Indiana',
    'Illinois', 'Wisconsin', 'New York', 'Connecticut', 'Massachusetts',
    'Rhode Island', 'Vermont', 'New Hampshire', 'Maine'
}

WEST_COAST_COUNTRIES = {
    'United States', 'Canada', 'Mexico', 'Cuba', 'Brazil', 'Argentina',
    'Chile', 'Colombia', 'Peru', 'Venezuela', 'Ecuador', 'Bolivia', 'Paraguay',
    'Uruguay', 'Suriname', 'Guyana', 'French Guiana', 'Falkland Islands'
}

# Germany node - Europe and Africa
GERMANY_STATES = {
    'Baden-Wurttemberg', 'Bavaria', 'Berlin', 'Brandenburg', 'Bremen',
    'Hamburg', 'Hesse', 'Lower Saxony', 'Mecklenburg-Vorpommern',
    'North Rhine-Westphalia', 'Rhineland-Palatinate', 'Saarland',
    'Saxony', 'Saxony-Anhalt', 'Schleswig-Holstein', 'Thuringia',
    'Vienna', 'Attica', 'Barcelona', 'A Coruña', 'Alberta'
}

GERMANY_COUNTRIES = {
    'Germany', 'France', 'United Kingdom', 'Ireland', 'Netherlands', 'Belgium',
    'Luxembourg', 'Switzerland', 'Austria', 'Poland', 'Czech Republic',
    'Slovakia', 'Hungary', 'Slovenia', 'Croatia', 'Bosnia and Herzegovina',
    'Serbia', 'Montenegro', 'Kosovo', 'North Macedonia', 'Albania', 'Greece',
    'Bulgaria', 'Romania', 'Moldova', 'Ukraine', 'Belarus', 'Lithuania',
    'Latvia', 'Estonia', 'Finland', 'Sweden', 'Norway', 'Denmark', 'Iceland',
    'Portugal', 'Spain', 'Italy', 'Malta', 'Monaco', 'San Marino', 'Vatican City',
    'Andorra', 'Liechtenstein', 'Morocco', 'Algeria', 'Tunisia', 'Libya', 'Egypt',
    'Sudan', 'South Sudan', 'Eritrea', 'Djibouti', 'Ethiopia', 'Somalia',
    'Kenya', 'Tanzania', 'Uganda', 'Rwanda', 'Burundi', 'Democratic Republic of the Congo',
    'Republic of the Congo', 'Gabon', 'Equatorial Guinea', 'Cameroon', 'Central African Republic',
    'Chad', 'Niger', 'Mali', 'Burkina Faso', 'Ghana', 'Togo', 'Benin', 'Nigeria'
}

# Israel node - Middle East and Asia
ISRAEL_STATES = {
    'Almaty', 'Tatarstan Republic', 'Belgorod Oblast', 'İzmir Province',
    'Bacău County', 'Giza', 'Saida', 'Seoul', 'Fujian', 'Sichuan', 'Mecca Region',
    'Auckland'
}

ISRAEL_COUNTRIES = {
    'Israel', 'Turkey', 'Saudi Arabia', 'United Arab Emirates', 'Qatar', 'Kuwait',
    'Bahrain', 'Oman', 'Yemen', 'Jordan', 'Lebanon', 'Syria', 'Iraq', 'Iran',
    'Afghanistan', 'Pakistan', 'India', 'Bangladesh', 'Sri Lanka', 'Nepal',
    'Bhutan', 'Maldives', 'China', 'Japan', 'South Korea', 'North Korea',
    'Taiwan', 'Hong Kong', 'Macau', 'Mongolia', 'Thailand', 'Cambodia', 'Laos',
    'Vietnam', 'Myanmar', 'Malaysia', 'Singapore', 'Indonesia', 'Philippines',
    'Brunei', 'East Timor', 'Australia', 'New Zealand', 'Papua New Guinea',
    'Solomon Islands', 'Vanuatu', 'Fiji', 'Samoa', 'Tonga', 'Tuvalu', 'Kiribati',
    'Marshall Islands', 'Micronesia', 'Palau', 'Nauru', 'Kazakhstan', 'Kyrgyzstan',
    'Tajikistan', 'Turkmenistan', 'Uzbekistan', 'Russia', 'Armenia', 'Azerbaijan',
    'Georgia'
}

# Create async clients for both endpoints
clients: Dict[str, AsyncOpenAI] = {}


def init_clients(model: str):
    """Initialize OpenAI clients for all endpoints."""
    global clients
    for region, endpoint in ENDPOINTS.items():
        clients[region] = AsyncOpenAI(
            base_url=endpoint['base_url'],
            api_key='not-needed',  # vLLM doesn't require API key
        )
    print("Initialized clients for endpoints:")
    for region, endpoint in ENDPOINTS.items():
        print(f"  - {region}: {endpoint['base_url']}")


def get_region_for_conv(conv: Dict[str, Any]) -> str:
    """Determine which endpoint to route to based on geographic location."""
    state = conv.get('state', '')
    country = conv.get('country', '')

    # Route to West Coast node (California)
    if state in WEST_COAST_STATES or country in WEST_COAST_COUNTRIES:
        return 'west_coast'

    # Route to Germany node (Europe/Africa)
    if state in GERMANY_STATES or country in GERMANY_COUNTRIES:
        return 'germany'

    # Route to Israel node (Middle East/Asia/Oceania)
    if state in ISRAEL_STATES or country in ISRAEL_COUNTRIES:
        return 'israel'

    # Default fallback to West Coast for unmapped locations
    return 'west_coast'


def percentile(values: List[float], p: float) -> float:
    """Calculate the p-th percentile of a list of values."""
    if not values:
        return 0.0
    sorted_values = sorted(values)
    idx = int(len(sorted_values) * p / 100)
    idx = min(idx, len(sorted_values) - 1)
    return sorted_values[idx]


@dataclass
class StreamingResult:
    """Result from a streaming chat completion call."""
    content: str
    ttft: float  # Time to First Token
    total_latency: float  # End-to-end latency
    itl_avg: float  # Average Inter-Token Latency
    itl_p50: float  # P50 Inter-Token Latency
    itl_p99: float  # P99 Inter-Token Latency
    num_tokens: int  # Number of tokens generated
    success: bool
    error: Optional[str] = None


async def call_chat_completion(
    messages: List[Dict[str, str]],
    region: str,
    model: str,
    temperature: float = 0.0,
    max_tokens: int = 512,
) -> StreamingResult:
    """Call chat completion with streaming to capture TTFT and ITL metrics."""
    client = clients[region]
    st_request = time.time()
    
    ttft = None
    token_times: List[float] = []  # Timestamps for each token
    content_parts: List[str] = []
    
    try:
        stream = await client.chat.completions.create(
            model=model,
            messages=messages,
            temperature=temperature,
            max_tokens=max_tokens,
            stream=True,  # Enable streaming for TTFT measurement
        )
        
        async for chunk in stream:
            now = time.time()
            
            # Check if this chunk has content
            if chunk.choices and chunk.choices[0].delta.content:
                content = chunk.choices[0].delta.content
                content_parts.append(content)
                
                # Capture TTFT on the very first token
                if ttft is None:
                    ttft = now - st_request
                
                # Record token arrival time for ITL calculation
                token_times.append(now)
        
        total_latency = time.time() - st_request
        
        # Calculate ITL (Inter-Token Latency) statistics
        itl_values: List[float] = []
        if len(token_times) >= 2:
            for i in range(1, len(token_times)):
                itl_values.append(token_times[i] - token_times[i - 1])
        
        itl_avg = sum(itl_values) / len(itl_values) if itl_values else 0.0
        itl_p50 = percentile(itl_values, 50) if itl_values else 0.0
        itl_p99 = percentile(itl_values, 99) if itl_values else 0.0
        
        return StreamingResult(
            content=''.join(content_parts),
            ttft=ttft or total_latency,  # Fallback if no tokens received
            total_latency=total_latency,
            itl_avg=itl_avg,
            itl_p50=itl_p50,
            itl_p99=itl_p99,
            num_tokens=len(token_times),
            success=True,
        )
        
    except Exception as e:
        total_latency = time.time() - st_request
        print(f"Error calling {region} endpoint: {e}")
        return StreamingResult(
            content='',
            ttft=0.0,
            total_latency=total_latency,
            itl_avg=0.0,
            itl_p50=0.0,
            itl_p99=0.0,
            num_tokens=0,
            success=False,
            error=str(e),
        )


def _load_dataset(start_index: int, max_load: int = 0) -> List[Dict[str, Any]]:
    """Load and filter the WildChat dataset."""
    tic = time.time()
    if max_load > 0:
        split_slice = f'{start_index}:{start_index + max_load}'
    else:
        # Load all available data from start_index onwards (for max load testing)
        split_slice = f'{start_index}:'
    print(f"Loading dataset slice: train[{split_slice}]...")
    chunk_data = datasets.load_dataset(DATASET_NAME, split=f'train[{split_slice}]')
    
    multi_turn_data = []
    for d in chunk_data:
        # At least 2 full turns: user + assistant + user + assistant (len >= 4)
        if d['turn'] >= 2 and isinstance(d['conversation'], list) and len(d['conversation']) >= 4:
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
    metrics_log: List[RequestMetrics],
) -> None:
    """Execute a multi-turn conversation with detailed metrics collection."""
    history = []
    region = get_region_for_conv(conv)
    
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
            request_timestamp = time.time()
            
            # Estimate input tokens (rough approximation: ~4 chars per token)
            input_chars = sum(len(m['content']) for m in history)
            input_tokens_est = input_chars // 4
            
            result = await call_chat_completion(
                messages=history,
                region=region,
                model=model,
            )
            
            # Create detailed metrics record
            metrics = RequestMetrics(
                timestamp=request_timestamp - tic,
                user_id=uid,
                conv_idx=idx,
                turn=i // 2 + 1,
                region=region,
                country=conv.get('country', ''),
                state=conv.get('state', ''),
                ttft=result.ttft,
                total_latency=result.total_latency,
                itl_avg=result.itl_avg,
                itl_p50=result.itl_p50,
                itl_p99=result.itl_p99,
                num_tokens=result.num_tokens,
                input_tokens=input_tokens_est,
                success=result.success,
                error=result.error,
            )
            metrics_log.append(metrics)
            
            if not result.success:
                stats['errors'] += 1
                return
            
            history.append({'role': 'assistant', 'content': result.content})
            
            # Update aggregate stats
            stats['requests'] += 1
            stats['total_latency'] += result.total_latency
            stats['total_ttft'] += result.ttft
            stats['total_itl'] += result.itl_avg
            stats['total_tokens'] += result.num_tokens
            stats[f'{region}_requests'] += 1
            stats[f'{region}_ttft'] += result.ttft
            stats['ttft_values'].append(result.ttft)
            stats['itl_values'].append(result.itl_avg)
            
            print(f'[{time.time() - tic:.2f}s] User {uid} conv {idx} turn {i//2+1} '
                  f'-> {region} (TTFT: {result.ttft*1000:.0f}ms, '
                  f'ITL: {result.itl_avg*1000:.1f}ms, '
                  f'total: {result.total_latency:.2f}s, '
                  f'{result.num_tokens} tokens)')


async def _user_task(
    duration: int,
    tic: float,
    uid: int,
    convs: List[Dict[str, Any]],
    model: str,
    stats: Dict[str, Any],
    metrics_log: List[RequestMetrics],
) -> None:
    """Run all conversations for a single user."""
    for i, conv in enumerate(convs):
        elapsed = time.time() - tic
        if elapsed >= duration:
            break
        
        try:
            await _multi_turn_conv(uid, i, duration, tic, conv, model, stats, metrics_log)
        except asyncio.CancelledError:
            break
        except Exception as e:
            print(f'User {uid}: Error in conversation {i}: {e}')
            stats['errors'] += 1


async def run_benchmark(
    duration: int = 30,
    num_users: int = 32,
    start_index: int = 0,
    model: str = 'default',
    max_convs: int = 0,
    output_dir: str = 'benchmark_results',
    backend_port: int = 8080,
    skip_cache_flush: bool = False,
) -> Dict[str, Any]:
    """Run the benchmark with the specified parameters."""
    
    # Initialize clients
    init_clients(model)
    
    # Flush KV caches on all backends before benchmark for accurate TTFT
    if not skip_cache_flush:
        await flush_all_kv_caches(backend_port)
    else:
        print("\nSkipping KV cache flush (--skip-cache-flush specified)")
    
    # Load dataset - for unlimited mode, load a large slice for max throughput testing
    if max_convs == 0:
        # Load 50k conversations for unlimited load testing
        convs = _load_dataset(start_index, max_load=50000)
    else:
        # Load limited amount
        convs = _load_dataset(start_index, max_load=max_convs * 3)
    
    # Limit conversations if specified (0 = unlimited for load testing)
    if max_convs > 0 and len(convs) > max_convs:
        convs = convs[:max_convs]
        print(f"Limited to {max_convs} conversations")
    else:
        print(f"Using all {len(convs)} conversations (no limit)")
    
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
    
    # Count geographic distribution
    west_coast_count = sum(1 for c in convs if get_region_for_conv(c) == 'west_coast')
    germany_count = sum(1 for c in convs if get_region_for_conv(c) == 'germany')
    israel_count = sum(1 for c in convs if get_region_for_conv(c) == 'israel')
    print(f'Geographic routing: {west_coast_count} west_coast, {germany_count} germany, {israel_count} israel')
    
    # Initialize stats with TTFT/ITL tracking
    stats = {
        'requests': 0,
        'errors': 0,
        'total_latency': 0.0,
        'total_ttft': 0.0,
        'total_itl': 0.0,
        'total_tokens': 0,
        'west_coast_requests': 0,
        'west_coast_ttft': 0.0,
        'germany_requests': 0,
        'germany_ttft': 0.0,
        'israel_requests': 0,
        'israel_ttft': 0.0,
        'ttft_values': [],  # For percentile calculations
        'itl_values': [],  # For percentile calculations
    }
    
    # Metrics log for detailed per-request recording
    metrics_log: List[RequestMetrics] = []
    
    # Run benchmark
    print(f'\n{"="*60}')
    print(f'Starting benchmark for {duration}s with {num_users} concurrent users')
    print(f'{"="*60}\n')
    
    tic = time.time()
    benchmark_start = datetime.now()
    
    tasks = []
    for uid, user_convs in groups.items():
        if user_convs:
            user_convs.sort(key=lambda c: c['timestamp'])
            tasks.append(_user_task(duration, tic, uid, user_convs, model, stats, metrics_log))
    
    await asyncio.gather(*tasks, return_exceptions=True)
    
    total_time = time.time() - tic
    
    # Calculate percentiles
    ttft_p50 = percentile(stats['ttft_values'], 50) if stats['ttft_values'] else 0.0
    ttft_p90 = percentile(stats['ttft_values'], 90) if stats['ttft_values'] else 0.0
    ttft_p99 = percentile(stats['ttft_values'], 99) if stats['ttft_values'] else 0.0
    
    itl_p50 = percentile(stats['itl_values'], 50) if stats['itl_values'] else 0.0
    itl_p90 = percentile(stats['itl_values'], 90) if stats['itl_values'] else 0.0
    itl_p99 = percentile(stats['itl_values'], 99) if stats['itl_values'] else 0.0
    
    # Print results
    print(f'\n{"="*60}')
    print('BENCHMARK RESULTS')
    print(f'{"="*60}')
    print(f'Duration: {total_time:.2f}s')
    print(f'Total requests: {stats["requests"]}')
    print(f'  - West Coast endpoint: {stats["west_coast_requests"]}')
    print(f'  - Germany endpoint: {stats["germany_requests"]}')
    print(f'  - Israel endpoint: {stats["israel_requests"]}')
    print(f'Errors: {stats["errors"]}')
    
    if stats['requests'] > 0:
        avg_ttft = stats['total_ttft'] / stats['requests']
        avg_itl = stats['total_itl'] / stats['requests']
        avg_latency = stats['total_latency'] / stats['requests']
        avg_tokens = stats['total_tokens'] / stats['requests']
        
        print(f'\n--- Latency Metrics ---')
        print(f'Avg end-to-end latency: {avg_latency:.2f}s')
        print(f'Throughput: {stats["requests"] / total_time:.2f} req/s')
        print(f'Total tokens generated: {stats["total_tokens"]}')
        print(f'Avg tokens per request: {avg_tokens:.1f}')
        
        print(f'\n--- TTFT (Time to First Token) ---')
        print(f'  Avg:  {avg_ttft*1000:.1f}ms')
        print(f'  P50:  {ttft_p50*1000:.1f}ms')
        print(f'  P90:  {ttft_p90*1000:.1f}ms')
        print(f'  P99:  {ttft_p99*1000:.1f}ms')
        
        print(f'\n--- ITL (Inter-Token Latency) ---')
        print(f'  Avg:  {avg_itl*1000:.2f}ms')
        print(f'  P50:  {itl_p50*1000:.2f}ms')
        print(f'  P90:  {itl_p90*1000:.2f}ms')
        print(f'  P99:  {itl_p99*1000:.2f}ms')
        
        # Per-region TTFT breakdown
        print(f'\n--- Per-Region Avg TTFT ---')
        for region in ['west_coast', 'germany', 'israel']:
            region_reqs = stats[f'{region}_requests']
            if region_reqs > 0:
                region_ttft = stats[f'{region}_ttft'] / region_reqs
                print(f'  {region}: {region_ttft*1000:.1f}ms ({region_reqs} reqs)')
    
    # Save detailed metrics to JSONL file
    os.makedirs(output_dir, exist_ok=True)
    timestamp_str = benchmark_start.strftime('%Y%m%d_%H%M%S')
    metrics_file = os.path.join(output_dir, f'metrics_{timestamp_str}.jsonl')
    
    with open(metrics_file, 'w') as f:
        for m in metrics_log:
            f.write(json.dumps(asdict(m)) + '\n')
    
    print(f'\nDetailed metrics saved to: {metrics_file}')
    
    # Save summary to JSON
    summary = {
        'benchmark_start': benchmark_start.isoformat(),
        'duration_seconds': total_time,
        'num_users': num_users,
        'total_requests': stats['requests'],
        'errors': stats['errors'],
        'throughput_rps': stats['requests'] / total_time if total_time > 0 else 0,
        'total_tokens': stats['total_tokens'],
        'avg_tokens_per_request': stats['total_tokens'] / stats['requests'] if stats['requests'] > 0 else 0,
        'latency': {
            'avg_ms': avg_latency * 1000 if stats['requests'] > 0 else 0,
        },
        'ttft': {
            'avg_ms': avg_ttft * 1000 if stats['requests'] > 0 else 0,
            'p50_ms': ttft_p50 * 1000,
            'p90_ms': ttft_p90 * 1000,
            'p99_ms': ttft_p99 * 1000,
        },
        'itl': {
            'avg_ms': avg_itl * 1000 if stats['requests'] > 0 else 0,
            'p50_ms': itl_p50 * 1000,
            'p90_ms': itl_p90 * 1000,
            'p99_ms': itl_p99 * 1000,
        },
        'per_region': {
            'west_coast': {
                'requests': stats['west_coast_requests'],
                'avg_ttft_ms': (stats['west_coast_ttft'] / stats['west_coast_requests'] * 1000) 
                               if stats['west_coast_requests'] > 0 else 0,
            },
            'germany': {
                'requests': stats['germany_requests'],
                'avg_ttft_ms': (stats['germany_ttft'] / stats['germany_requests'] * 1000)
                               if stats['germany_requests'] > 0 else 0,
            },
            'israel': {
                'requests': stats['israel_requests'],
                'avg_ttft_ms': (stats['israel_ttft'] / stats['israel_requests'] * 1000)
                               if stats['israel_requests'] > 0 else 0,
            },
        },
    }
    
    summary_file = os.path.join(output_dir, f'summary_{timestamp_str}.json')
    with open(summary_file, 'w') as f:
        json.dump(summary, f, indent=2)
    
    print(f'Summary saved to: {summary_file}')
    
    return stats


def main():
    parser = argparse.ArgumentParser(description='WildChat benchmark with gotoni cluster - TTFT/ITL metrics')
    parser.add_argument('--duration', type=int, default=30, help='Benchmark duration in seconds')
    parser.add_argument('--num-users', type=int, default=32, help='Number of concurrent users (higher = more load)')
    parser.add_argument('--start-index', type=int, default=0, help='Dataset chunk index (0-9)')
    parser.add_argument('--model', type=str, default='default', help='Model name to use')
    parser.add_argument('--max-convs', type=int, default=0, help='Max conversations per user (0 = unlimited for max load testing)')
    parser.add_argument('--west-coast-ip', type=str, default='192.9.140.201', help='West coast endpoint IP')
    parser.add_argument('--germany-ip', type=str, default='130.61.109.93', help='Germany endpoint IP')
    parser.add_argument('--israel-ip', type=str, default='151.145.83.16', help='Israel endpoint IP')
    parser.add_argument('--port', type=int, default=8000, help='Load balancer port (for API requests)')
    parser.add_argument('--backend-port', type=int, default=8080, help='SGLang backend port (for cache flush)')
    parser.add_argument('--output-dir', type=str, default='benchmark_results', 
                        help='Directory to save detailed metrics (JSONL) and summary (JSON)')
    parser.add_argument('--skip-cache-flush', action='store_true',
                        help='Skip flushing KV caches before benchmark (not recommended for accurate TTFT)')

    args = parser.parse_args()

    # Update endpoints if custom IPs provided
    ENDPOINTS['west_coast']['ip'] = args.west_coast_ip
    ENDPOINTS['west_coast']['base_url'] = f'http://{args.west_coast_ip}:{args.port}/v1'
    ENDPOINTS['germany']['ip'] = args.germany_ip
    ENDPOINTS['germany']['base_url'] = f'http://{args.germany_ip}:{args.port}/v1'
    ENDPOINTS['israel']['ip'] = args.israel_ip
    ENDPOINTS['israel']['base_url'] = f'http://{args.israel_ip}:{args.port}/v1'
    
    asyncio.run(run_benchmark(
        duration=args.duration,
        num_users=args.num_users,
        start_index=args.start_index,
        model=args.model,
        max_convs=args.max_convs,
        output_dir=args.output_dir,
        backend_port=args.backend_port,
        skip_cache_flush=args.skip_cache_flush,
    ))


if __name__ == '__main__':
    main()
