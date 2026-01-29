"""Parse GuideLLM benchmarks.json into clean CSV files for Google Sheets.

Creates two CSV files:
1. requests.csv - Per-request metrics (one row per request)
2. summary.csv - Aggregate statistics

Usage:
    python parse_guidellm_results.py [path/to/benchmarks.json]

    Default: ./guidellm_results/benchmarks.json
"""
import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Any, Dict, List


def flatten_request(req: Dict[str, Any], benchmark_idx: int = 0) -> Dict[str, Any]:
    """Flatten a single request into a flat dictionary for CSV."""
    flat = {
        "benchmark_idx": benchmark_idx,
        "request_id": req.get("request_id"),
        "request_type": req.get("request_type"),

        # Timing metrics
        "request_start_time": req.get("request_start_time"),
        "request_end_time": req.get("request_end_time"),
        "request_latency_ms": req.get("request_latency"),
        "time_to_first_token_ms": req.get("time_to_first_token_ms"),
        "time_per_output_token_ms": req.get("time_per_output_token_ms"),

        # Token counts
        "prompt_tokens": req.get("prompt_tokens"),
        "output_tokens": req.get("output_tokens"),
        "total_tokens": req.get("total_tokens"),

        # Throughput
        "tokens_per_second": req.get("tokens_per_second"),
        "output_tokens_per_second": req.get("output_tokens_per_second"),

        # Inter-token latency stats
        "itl_mean_ms": None,
        "itl_median_ms": None,
        "itl_p90_ms": None,
        "itl_p99_ms": None,
    }

    # Extract inter-token latency if available
    itl = req.get("inter_token_latency_ms")
    if itl and isinstance(itl, dict):
        flat["itl_mean_ms"] = itl.get("mean")
        flat["itl_median_ms"] = itl.get("median")
        percentiles = itl.get("percentiles", {})
        flat["itl_p90_ms"] = percentiles.get("p90")
        flat["itl_p99_ms"] = percentiles.get("p99")

    return flat


def extract_percentile_row(metric_name: str, metric_data: Dict[str, Any]) -> Dict[str, Any]:
    """Extract a row for the summary CSV from a metric with percentiles."""
    if not metric_data or not isinstance(metric_data, dict):
        return None

    successful = metric_data.get("successful", metric_data)
    if not isinstance(successful, dict):
        return None

    percentiles = successful.get("percentiles", {})

    return {
        "metric": metric_name,
        "count": successful.get("count"),
        "mean": successful.get("mean"),
        "median": successful.get("median"),
        "std_dev": successful.get("std_dev"),
        "min": successful.get("min"),
        "max": successful.get("max"),
        "p50": percentiles.get("p50"),
        "p90": percentiles.get("p90"),
        "p95": percentiles.get("p95"),
        "p99": percentiles.get("p99"),
    }


def parse_benchmarks(json_path: str, output_dir: str = None):
    """Parse benchmarks.json and create CSV files."""
    json_path = Path(json_path)
    if not json_path.exists():
        print(f"Error: File not found: {json_path}")
        sys.exit(1)

    if output_dir is None:
        output_dir = json_path.parent
    else:
        output_dir = Path(output_dir)
        output_dir.mkdir(parents=True, exist_ok=True)

    print(f"Loading {json_path}...")
    with open(json_path) as f:
        data = json.load(f)

    benchmarks = data.get("benchmarks", [])
    if not benchmarks:
        print("Error: No benchmarks found in JSON")
        sys.exit(1)

    # ==========================================================================
    # Create requests.csv - one row per request
    # ==========================================================================
    all_requests: List[Dict[str, Any]] = []

    for bench_idx, benchmark in enumerate(benchmarks):
        requests_data = benchmark.get("requests", {})

        # Process successful requests
        for req in requests_data.get("successful", []):
            flat = flatten_request(req, bench_idx)
            flat["status"] = "successful"
            all_requests.append(flat)

        # Process errored requests
        for req in requests_data.get("errored", []):
            flat = flatten_request(req, bench_idx)
            flat["status"] = "errored"
            all_requests.append(flat)

        # Process incomplete requests
        for req in requests_data.get("incomplete", []):
            flat = flatten_request(req, bench_idx)
            flat["status"] = "incomplete"
            all_requests.append(flat)

    if all_requests:
        requests_csv = output_dir / "requests_clean.csv"
        fieldnames = list(all_requests[0].keys())

        with open(requests_csv, "w", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=fieldnames)
            writer.writeheader()
            writer.writerows(all_requests)

        print(f"Wrote {len(all_requests)} requests to {requests_csv}")

    # ==========================================================================
    # Create summary.csv - aggregate statistics
    # ==========================================================================
    summary_rows: List[Dict[str, Any]] = []

    for bench_idx, benchmark in enumerate(benchmarks):
        metrics = benchmark.get("metrics", {})

        # Key metrics to extract
        metric_names = [
            ("time_to_first_token_ms", "TTFT (ms)"),
            ("inter_token_latency_ms", "Inter-Token Latency (ms)"),
            ("request_latency", "Request Latency (ms)"),
            ("time_per_output_token_ms", "Time per Output Token (ms)"),
            ("tokens_per_second", "Tokens/sec"),
            ("output_tokens_per_second", "Output Tokens/sec"),
            ("prompt_tokens_per_second", "Prompt Tokens/sec"),
            ("requests_per_second", "Requests/sec"),
            ("output_token_count", "Output Token Count"),
            ("prompt_token_count", "Prompt Token Count"),
            ("total_token_count", "Total Token Count"),
        ]

        for metric_key, metric_label in metric_names:
            metric_data = metrics.get(metric_key)
            if metric_data:
                row = extract_percentile_row(metric_label, metric_data)
                if row:
                    row["benchmark_idx"] = bench_idx
                    summary_rows.append(row)

    if summary_rows:
        summary_csv = output_dir / "summary_clean.csv"
        fieldnames = ["benchmark_idx", "metric", "count", "mean", "median",
                      "std_dev", "min", "max", "p50", "p90", "p95", "p99"]

        with open(summary_csv, "w", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=fieldnames)
            writer.writeheader()
            writer.writerows(summary_rows)

        print(f"Wrote {len(summary_rows)} summary rows to {summary_csv}")

    # ==========================================================================
    # Create config.csv - benchmark configuration
    # ==========================================================================
    config_rows: List[Dict[str, Any]] = []

    for bench_idx, benchmark in enumerate(benchmarks):
        config = benchmark.get("config", {})
        row = {
            "benchmark_idx": bench_idx,
            "start_time": benchmark.get("start_time"),
            "end_time": benchmark.get("end_time"),
            "duration": benchmark.get("duration"),
            "warmup_duration": benchmark.get("warmup_duration"),
            "cooldown_duration": benchmark.get("cooldown_duration"),
        }

        # Flatten config
        for key, value in config.items():
            if isinstance(value, (str, int, float, bool, type(None))):
                row[f"config_{key}"] = value

        config_rows.append(row)

    if config_rows:
        config_csv = output_dir / "config_clean.csv"
        fieldnames = list(config_rows[0].keys())

        with open(config_csv, "w", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=fieldnames)
            writer.writeheader()
            writer.writerows(config_rows)

        print(f"Wrote {len(config_rows)} config rows to {config_csv}")

    print(f"\nDone! CSV files written to {output_dir}")
    print("\nFiles created:")
    print(f"  - requests_clean.csv  : Per-request metrics ({len(all_requests)} rows)")
    print(f"  - summary_clean.csv   : Aggregate statistics ({len(summary_rows)} rows)")
    print(f"  - config_clean.csv    : Benchmark configuration ({len(config_rows)} rows)")


def main():
    parser = argparse.ArgumentParser(
        description="Parse GuideLLM benchmarks.json into clean CSV files"
    )
    parser.add_argument(
        "json_file",
        nargs="?",
        default="guidellm_results/benchmarks.json",
        help="Path to benchmarks.json (default: guidellm_results/benchmarks.json)"
    )
    parser.add_argument(
        "--output-dir", "-o",
        help="Output directory for CSV files (default: same as JSON file)"
    )

    args = parser.parse_args()
    parse_benchmarks(args.json_file, args.output_dir)


if __name__ == "__main__":
    main()
