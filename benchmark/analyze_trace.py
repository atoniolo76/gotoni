#!/usr/bin/env python3
"""
analyze_trace.py - Analyze Perfetto traces from the load balancer

This script parses the JSON trace output from /lb/trace/stop and generates:
1. ASCII waterfall diagram showing request lifecycle
2. Bottleneck analysis identifying slowest components
3. Summary statistics

Usage:
    # Fetch and analyze trace from a running LB
    curl http://192.9.140.201:8000/lb/trace/stop | python analyze_trace.py
    
    # Analyze a saved trace file
    python analyze_trace.py trace.json
    
    # Or use the built-in fetch
    python analyze_trace.py --fetch http://192.9.140.201:8000
"""

import json
import sys
import argparse
from collections import defaultdict
from typing import Dict, List, Tuple, Optional
import urllib.request

def load_trace(source: str) -> dict:
    """Load trace from file or stdin"""
    if source == '-':
        return json.load(sys.stdin)
    elif source.startswith('http'):
        # Fetch from URL
        with urllib.request.urlopen(f"{source}/lb/trace/stop") as response:
            return json.loads(response.read().decode())
    else:
        with open(source, 'r') as f:
            return json.load(f)

def parse_events(trace: dict) -> Dict[int, List[dict]]:
    """Group events by thread ID (request)"""
    events = trace.get('traceEvents', [])
    
    # Group by tid (thread ID = request)
    by_request = defaultdict(list)
    for event in events:
        tid = event.get('tid', 0)
        by_request[tid].append(event)
    
    # Sort each request's events by timestamp
    for tid in by_request:
        by_request[tid].sort(key=lambda e: e.get('ts', 0))
    
    return by_request

def compute_span_durations(events: List[dict]) -> List[Tuple[str, float, float, float]]:
    """
    Compute duration for each span (B/E pair or X event)
    Returns: [(name, start_ts, end_ts, duration_ms), ...]
    """
    spans = []
    open_spans = {}  # name -> start_ts
    
    for event in events:
        name = event.get('name', 'unknown')
        ph = event.get('ph', '')
        ts = event.get('ts', 0)  # microseconds
        
        if ph == 'B':  # Begin
            open_spans[name] = ts
        elif ph == 'E':  # End
            if name in open_spans:
                start_ts = open_spans.pop(name)
                duration_ms = (ts - start_ts) / 1000.0
                spans.append((name, start_ts, ts, duration_ms))
        elif ph == 'X':  # Complete (has dur)
            dur = event.get('dur', 0)
            duration_ms = dur / 1000.0
            spans.append((name, ts, ts + dur, duration_ms))
        elif ph == 'i':  # Instant event
            spans.append((name, ts, ts, 0))
    
    return sorted(spans, key=lambda s: s[1])

def draw_waterfall(request_id: int, spans: List[Tuple[str, float, float, float]], max_width: int = 80):
    """Draw ASCII waterfall diagram for a request"""
    if not spans:
        return
    
    # Find time range
    min_ts = min(s[1] for s in spans)
    max_ts = max(s[2] for s in spans)
    time_range = max_ts - min_ts if max_ts > min_ts else 1
    
    # Calculate scale (microseconds per character)
    bar_width = max_width - 35  # Leave room for labels
    scale = time_range / bar_width if bar_width > 0 else 1
    
    print(f"\n{'='*max_width}")
    print(f"Request {request_id} - Total: {time_range/1000:.1f}ms")
    print(f"{'='*max_width}")
    
    # Draw each span
    for name, start, end, duration in spans:
        # Calculate bar position
        start_pos = int((start - min_ts) / scale) if scale > 0 else 0
        end_pos = int((end - min_ts) / scale) if scale > 0 else start_pos
        bar_len = max(1, end_pos - start_pos)
        
        # Truncate name
        label = name[:20].ljust(20)
        
        # Draw bar
        bar = ' ' * start_pos + '█' * bar_len
        
        # Format duration
        if duration > 0:
            dur_str = f"{duration:6.1f}ms"
        else:
            dur_str = "  (instant)"
        
        print(f"{label} |{bar[:bar_width]} {dur_str}")
    
    print(f"{'='*max_width}")

def analyze_bottlenecks(all_spans: Dict[str, List[float]]) -> List[Tuple[str, float, float, int]]:
    """
    Analyze which components are bottlenecks
    Returns: [(name, avg_ms, max_ms, count), ...]
    """
    results = []
    for name, durations in all_spans.items():
        if durations:
            avg_ms = sum(durations) / len(durations)
            max_ms = max(durations)
            results.append((name, avg_ms, max_ms, len(durations)))
    
    return sorted(results, key=lambda x: -x[1])  # Sort by avg duration desc

def main():
    parser = argparse.ArgumentParser(description='Analyze Perfetto traces from load balancer')
    parser.add_argument('source', nargs='?', default='-', 
                        help='Trace file, URL, or - for stdin (default: stdin)')
    parser.add_argument('--fetch', metavar='URL', 
                        help='Fetch trace from LB endpoint (e.g., http://192.9.140.201:8000)')
    parser.add_argument('--top', type=int, default=5, 
                        help='Show top N requests by duration')
    parser.add_argument('--all', action='store_true', 
                        help='Show all requests (not just top N)')
    args = parser.parse_args()
    
    # Load trace
    source = args.fetch if args.fetch else args.source
    try:
        trace = load_trace(source)
    except Exception as e:
        print(f"Error loading trace: {e}", file=sys.stderr)
        sys.exit(1)
    
    # Print metadata
    metadata = trace.get('metadata', {})
    print("\n" + "="*60)
    print("TRACE ANALYSIS")
    print("="*60)
    print(f"Session ID:   {metadata.get('session_id', 'unknown')}")
    print(f"Node ID:      {metadata.get('node_id', 'unknown')}")
    print(f"Duration:     {metadata.get('duration_ms', 0):.1f}ms")
    print(f"Total Events: {metadata.get('event_count', 0)}")
    print(f"Remote Events:{metadata.get('remote_count', 0)}")
    
    # Parse events by request
    by_request = parse_events(trace)
    print(f"Requests:     {len(by_request)}")
    
    # Compute spans for each request
    request_spans = {}
    all_spans = defaultdict(list)  # name -> [durations]
    
    for tid, events in by_request.items():
        if tid == 0:  # Skip trace session events
            continue
        spans = compute_span_durations(events)
        request_spans[tid] = spans
        
        # Aggregate for bottleneck analysis
        for name, _, _, duration in spans:
            if duration > 0:
                all_spans[name].append(duration)
    
    # Find total duration per request
    request_durations = []
    for tid, spans in request_spans.items():
        if spans:
            total_ms = sum(s[3] for s in spans)
            request_durations.append((tid, total_ms, spans))
    
    request_durations.sort(key=lambda x: -x[1])  # Sort by duration desc
    
    # Draw waterfall for top N requests
    print("\n" + "="*60)
    print("REQUEST WATERFALLS (slowest first)")
    print("="*60)
    
    shown = 0
    for tid, total_ms, spans in request_durations:
        if not args.all and shown >= args.top:
            break
        draw_waterfall(tid, spans)
        shown += 1
    
    if not args.all and len(request_durations) > args.top:
        print(f"\n... and {len(request_durations) - args.top} more requests")
        print(f"Use --all to see all requests")
    
    # Bottleneck analysis
    print("\n" + "="*60)
    print("BOTTLENECK ANALYSIS")
    print("="*60)
    print(f"{'Component':<25} {'Avg(ms)':>10} {'Max(ms)':>10} {'Count':>8}")
    print("-"*60)
    
    bottlenecks = analyze_bottlenecks(all_spans)
    for name, avg_ms, max_ms, count in bottlenecks[:10]:
        print(f"{name:<25} {avg_ms:>10.1f} {max_ms:>10.1f} {count:>8}")
    
    # Summary
    print("\n" + "="*60)
    print("SUMMARY")
    print("="*60)
    if request_durations:
        durations = [d[1] for d in request_durations]
        print(f"Request count:    {len(durations)}")
        print(f"Avg latency:      {sum(durations)/len(durations):.1f}ms")
        print(f"P50 latency:      {sorted(durations)[len(durations)//2]:.1f}ms")
        print(f"P99 latency:      {sorted(durations)[int(len(durations)*0.99)]:.1f}ms")
        print(f"Max latency:      {max(durations):.1f}ms")
    
    # Decision breakdown
    decisions = defaultdict(int)
    for tid, events in by_request.items():
        for event in events:
            if event.get('name') == 'capacity_check':
                args_data = event.get('args', {})
                if args_data.get('has_capacity'):
                    decisions['local'] += 1
                else:
                    decisions['forward'] += 1
    
    if decisions:
        print(f"\nRouting decisions:")
        for decision, count in sorted(decisions.items()):
            print(f"  {decision}: {count}")

if __name__ == '__main__':
    main()
