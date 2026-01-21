"""WildChat-1M multi-turn conversation workload with synthetic timestamps.

Adapted to use gotoni cluster endpoints with geographic routing.
"""

import argparse
import asyncio
import collections
import time
from typing import Any, Awaitable, Dict, List, Optional

import datasets
from openai import AsyncOpenAI

DATASET_NAME = 'allenai/WildChat-1M'

# Cluster endpoints
ENDPOINTS = {
    'west': {
        'name': 'llama-west',
        'ip': '209.20.159.183',
        'base_url': 'http://209.20.159.183:8000/v1',
    },
    'east': {
        'name': 'llama-east', 
        'ip': '129.213.93.186',
        'base_url': 'http://129.213.93.186:8000/v1',
    },
}

# Geographic routing - West coast states route to west, others to east
WEST_STATES = {
    'California', 'Oregon', 'Washington', 'Nevada', 'Arizona', 'Utah',
    'Colorado', 'New Mexico', 'Idaho', 'Montana', 'Wyoming', 'Alaska', 'Hawaii'
}

WEST_COUNTRIES = {
    'Japan', 'South Korea', 'China', 'Singapore', 'Thailand', 'Malaysia',
    'Philippines', 'Vietnam', 'Indonesia', 'India', 'Bangladesh', 'Sri Lanka',
    'Nepal', 'Pakistan', 'Mongolia', 'Brunei', 'Cambodia', 'Laos', 'Myanmar',
    'Australia', 'New Zealand', 'Taiwan', 'Hong Kong'
}

# Create async clients for both endpoints
clients: Dict[str, AsyncOpenAI] = {}


def init_clients(model: str):
    """Initialize OpenAI clients for both endpoints."""
    global clients
    for region, endpoint in ENDPOINTS.items():
        clients[region] = AsyncOpenAI(
            base_url=endpoint['base_url'],
            api_key='not-needed',  # vLLM doesn't require API key
        )
    print(f"Initialized clients for endpoints:")
    print(f"  - West: {ENDPOINTS['west']['base_url']}")
    print(f"  - East: {ENDPOINTS['east']['base_url']}")


def get_region_for_conv(conv: Dict[str, Any]) -> str:
    """Determine which endpoint to route to based on geographic location."""
    state = conv.get('state', '')
    country = conv.get('country', '')
    
    # Route west coast US states to west
    if state in WEST_STATES:
        return 'west'
    
    # Route Asian/Pacific countries to west
    if country in WEST_COUNTRIES:
        return 'west'
    
    # Everything else goes to east (US East, Europe, etc.)
    return 'east'


async def call_chat_completion(
    messages: List[Dict[str, str]],
    region: str,
    model: str,
    temperature: float = 0.0,
    max_tokens: int = 512,
) -> Optional[str]:
    """Call chat completion on the appropriate endpoint."""
    client = clients[region]
    try:
        response = await client.chat.completions.create(
            model=model,
            messages=messages,
            temperature=temperature,
            max_tokens=max_tokens,
        )
        return response.choices[0].message.content
    except Exception as e:
        print(f"Error calling {region} endpoint: {e}")
        return None


def _load_dataset(start_index: int) -> List[Dict[str, Any]]:
    """Load and filter the WildChat dataset."""
    tic = time.time()
    split_slice = f'{start_index*100000}:{(start_index+1)*100000}'
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
) -> None:
    """Execute a multi-turn conversation."""
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
            st_this_round = time.time()
            
            result = await call_chat_completion(
                messages=history,
                region=region,
                model=model,
            )
            
            if result is None:
                stats['errors'] += 1
                return
            
            history.append({'role': 'assistant', 'content': result})
            
            latency = time.time() - st_this_round
            stats['requests'] += 1
            stats['total_latency'] += latency
            stats[f'{region}_requests'] += 1
            
            print(f'[{time.time() - tic:.2f}s] User {uid} conv {idx} turn {i//2+1} '
                  f'-> {region} ({latency:.2f}s)')


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
    duration: int = 30,
    num_users: int = 4,
    start_index: int = 0,
    model: str = 'default',
    max_convs: int = 100,
) -> Dict[str, Any]:
    """Run the benchmark with the specified parameters."""
    
    # Initialize clients
    init_clients(model)
    
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
    
    # Count geographic distribution
    west_count = sum(1 for c in convs if get_region_for_conv(c) == 'west')
    east_count = len(convs) - west_count
    print(f'Geographic routing: {west_count} west, {east_count} east')
    
    # Initialize stats
    stats = {
        'requests': 0,
        'errors': 0,
        'total_latency': 0.0,
        'west_requests': 0,
        'east_requests': 0,
    }
    
    # Run benchmark
    print(f'\n{"="*60}')
    print(f'Starting benchmark for {duration}s with {num_users} concurrent users')
    print(f'{"="*60}\n')
    
    tic = time.time()
    
    tasks = []
    for uid, user_convs in groups.items():
        if user_convs:
            user_convs.sort(key=lambda c: c['timestamp'])
            tasks.append(_user_task(duration, tic, uid, user_convs, model, stats))
    
    await asyncio.gather(*tasks, return_exceptions=True)
    
    total_time = time.time() - tic
    
    # Print results
    print(f'\n{"="*60}')
    print('BENCHMARK RESULTS')
    print(f'{"="*60}')
    print(f'Duration: {total_time:.2f}s')
    print(f'Total requests: {stats["requests"]}')
    print(f'  - West endpoint: {stats["west_requests"]}')
    print(f'  - East endpoint: {stats["east_requests"]}')
    print(f'Errors: {stats["errors"]}')
    if stats['requests'] > 0:
        print(f'Avg latency: {stats["total_latency"] / stats["requests"]:.2f}s')
        print(f'Throughput: {stats["requests"] / total_time:.2f} req/s')
    
    return stats


def main():
    parser = argparse.ArgumentParser(description='WildChat benchmark with gotoni cluster')
    parser.add_argument('--duration', type=int, default=30, help='Benchmark duration in seconds')
    parser.add_argument('--num-users', type=int, default=4, help='Number of concurrent users')
    parser.add_argument('--start-index', type=int, default=0, help='Dataset chunk index (0-9)')
    parser.add_argument('--model', type=str, default='default', help='Model name to use')
    parser.add_argument('--max-convs', type=int, default=50, help='Max conversations for testing (0 for unlimited)')
    parser.add_argument('--west-ip', type=str, default='209.20.159.183', help='West endpoint IP')
    parser.add_argument('--east-ip', type=str, default='129.213.93.186', help='East endpoint IP')
    parser.add_argument('--port', type=int, default=8000, help='Endpoint port')
    
    args = parser.parse_args()
    
    # Update endpoints if custom IPs provided
    ENDPOINTS['west']['ip'] = args.west_ip
    ENDPOINTS['west']['base_url'] = f'http://{args.west_ip}:{args.port}/v1'
    ENDPOINTS['east']['ip'] = args.east_ip
    ENDPOINTS['east']['base_url'] = f'http://{args.east_ip}:{args.port}/v1'
    
    asyncio.run(run_benchmark(
        duration=args.duration,
        num_users=args.num_users,
        start_index=args.start_index,
        model=args.model,
        max_convs=args.max_convs,
    ))


if __name__ == '__main__':
    main()
