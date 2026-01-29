"""Geo-routing proxy for GuideLLM benchmarks.

This proxy:
1. Receives OpenAI-compatible requests from GuideLLM
2. Hashes the prompt to lookup user location from wildchat_location_lookup.json
3. Routes to the geographically nearest cluster node
4. Streams response back to GuideLLM
5. Logs detailed per-request routing decisions

Usage:
    python geo_proxy.py [--port 9000] [--lookup-file wildchat_location_lookup.json]

Then point GuideLLM at:
    guidellm benchmark --target http://localhost:9000/v1 ...
"""
import argparse
import asyncio
import hashlib
import json
import logging
import math
import time
import uuid
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from aiohttp import web, ClientSession, ClientTimeout

# =============================================================================
# Graceful Shutdown Configuration
# =============================================================================

SHUTDOWN_TIMEOUT = 30  # Max seconds to wait for in-flight requests during shutdown

# =============================================================================
# Configuration
# =============================================================================

# Cluster nodes: (name, ip, port, latitude, longitude)
CLUSTER_NODES = [
    ("gpu-2", "151.145.83.16", 8000, 31.0461, 34.8516),          # Israel (Tel Aviv)
    ("gotoni-germany", "130.61.109.93", 8000, 50.1109, 8.6821),  # Frankfurt, Germany
    ("west_coast", "192.9.140.201", 8000, 37.7749, -122.4194),   # San Francisco, USA
]

# Default location for unknown prompts (US center)
DEFAULT_LOCATION = {"lat": 39.8283, "lon": -98.5795, "country": "United States", "state": None}

# Request timeout
REQUEST_TIMEOUT = 120  # seconds

# =============================================================================
# Logging Setup
# =============================================================================

def setup_logging(log_file: str) -> logging.Logger:
    """Set up logging with both file and console output."""
    logger = logging.getLogger("geo_proxy")
    logger.setLevel(logging.INFO)

    # File handler (JSONL format for easy parsing)
    file_handler = logging.FileHandler(log_file)
    file_handler.setFormatter(logging.Formatter("%(message)s"))

    # Console handler (human readable)
    console_handler = logging.StreamHandler()
    console_handler.setFormatter(
        logging.Formatter("%(asctime)s | %(levelname)s | %(message)s")
    )

    logger.addHandler(file_handler)
    logger.addHandler(console_handler)

    return logger


# =============================================================================
# Geo Utilities
# =============================================================================

def hash_prompt(text: str) -> str:
    """Create a stable hash for prompt lookup (must match preprocess script)."""
    key = f"{text[:100]}:{len(text)}"
    return hashlib.md5(key.encode()).hexdigest()[:16]


def hash_messages(messages: list) -> str:
    """Create a stable hash for a multi-turn conversation (must match preprocess script)."""
    last_user_msg = ""
    for msg in reversed(messages):
        if msg.get("role") == "user":
            last_user_msg = msg.get("content", "")
            break

    key = f"{last_user_msg[:100]}:{len(messages)}:{len(last_user_msg)}"
    return hashlib.md5(key.encode()).hexdigest()[:16]


def haversine_distance(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    """Calculate great-circle distance between two points in km."""
    R = 6371  # Earth radius in km
    dlat = math.radians(lat2 - lat1)
    dlon = math.radians(lon2 - lon1)
    a = (
        math.sin(dlat / 2) ** 2
        + math.cos(math.radians(lat1))
        * math.cos(math.radians(lat2))
        * math.sin(dlon / 2) ** 2
    )
    return R * 2 * math.atan2(math.sqrt(a), math.sqrt(1 - a))


def find_nearest_node(
    lat: float, lon: float
) -> Tuple[str, str, int, float]:
    """Find the nearest cluster node to given coordinates.

    Returns: (node_name, ip, port, distance_km)
    """
    min_dist = float("inf")
    nearest = CLUSTER_NODES[0]

    for node in CLUSTER_NODES:
        name, ip, port, node_lat, node_lon = node
        dist = haversine_distance(lat, lon, node_lat, node_lon)
        if dist < min_dist:
            min_dist = dist
            nearest = node

    return nearest[0], nearest[1], nearest[2], min_dist


# =============================================================================
# Proxy Server
# =============================================================================

class GeoProxy:
    """OpenAI-compatible proxy with geo-routing."""

    def __init__(self, lookup_file: str, log_file: str):
        self.lookup: Dict[str, Dict[str, Any]] = {}
        self.logger = setup_logging(log_file)
        self.request_count = 0
        self.node_stats: Dict[str, int] = {node[0]: 0 for node in CLUSTER_NODES}

        # Graceful shutdown tracking
        self.active_requests = 0
        self.shutting_down = False
        self._shutdown_event = asyncio.Event()

        # Load location lookup
        lookup_path = Path(lookup_file)
        if lookup_path.exists():
            with open(lookup_path) as f:
                self.lookup = json.load(f)
            self.logger.info(f"Loaded {len(self.lookup)} location mappings from {lookup_file}")
        else:
            self.logger.warning(f"Lookup file not found: {lookup_file}")
            self.logger.warning("All requests will use default location (US center)")

    def extract_prompt_and_check_multiturn(self, body: Dict[str, Any]) -> Tuple[str, bool, Optional[list]]:
        """Extract user prompt from request body and detect multi-turn.

        Returns: (prompt_text, is_multi_turn, parsed_messages)
        """
        messages = body.get("messages", [])

        for msg in messages:
            if msg.get("role") == "user":
                content = msg.get("content", "")

                # Handle multimodal content (list of parts)
                if isinstance(content, list):
                    for part in content:
                        if isinstance(part, dict) and part.get("type") == "text":
                            content = part.get("text", "")
                            break
                    else:
                        return "", False, None

                # Check if content is a JSON array (multi-turn format)
                if content.startswith("[") and content.endswith("]"):
                    try:
                        parsed = json.loads(content)
                        if isinstance(parsed, list) and len(parsed) > 0:
                            # Verify it looks like a messages array
                            if all(isinstance(m, dict) and "role" in m for m in parsed):
                                return content, True, parsed
                    except json.JSONDecodeError:
                        pass

                return content, False, None

        return "", False, None

    def lookup_location(self, prompt: str, is_multi_turn: bool = False, messages: list = None) -> Dict[str, Any]:
        """Lookup location for a prompt hash."""
        if is_multi_turn and messages:
            h = hash_messages(messages)
        else:
            h = hash_prompt(prompt)
        return self.lookup.get(h, DEFAULT_LOCATION)

    async def handle_chat_completions(self, request: web.Request) -> web.StreamResponse:
        """Handle /v1/chat/completions with geo-routing."""
        # Reject new requests during shutdown
        if self.shutting_down:
            return web.json_response(
                {"error": {"message": "Server is shutting down", "type": "server_error"}},
                status=503
            )

        request_id = str(uuid.uuid4())[:8]
        start_time = time.time()
        self.request_count += 1
        self.active_requests += 1

        try:
            try:
                body = await request.json()
            except Exception as e:
                self.logger.error(f"[{request_id}] Failed to parse request body: {e}")
                return web.json_response(
                    {"error": {"message": str(e), "type": "invalid_request_error"}},
                    status=400
                )

            # Extract prompt and check for multi-turn
            prompt, is_multi_turn, parsed_messages = self.extract_prompt_and_check_multiturn(body)

            # If multi-turn, reconstruct the request body with proper messages
            if is_multi_turn and parsed_messages:
                body["messages"] = parsed_messages
                self.logger.info(f"[{request_id}] Multi-turn detected: {len(parsed_messages)} messages")

            # Lookup location
            loc = self.lookup_location(prompt, is_multi_turn, parsed_messages)

            # Find nearest node
            node_name, node_ip, node_port, distance_km = find_nearest_node(
                loc["lat"], loc["lon"]
            )
            target_url = f"http://{node_ip}:{node_port}/v1/chat/completions"

            self.node_stats[node_name] += 1

            # Log routing decision
            self.logger.info(
                f"[{request_id}] Routing to {node_name} | "
                f"loc=({loc['lat']:.2f}, {loc['lon']:.2f}) | "
                f"dist={distance_km:.0f}km | "
                f"country={loc.get('country', 'unknown')}"
            )

            # Forward request
            ttft = None
            total_tokens = 0
            status = "success"
            error_msg = None

            try:
                timeout = ClientTimeout(total=REQUEST_TIMEOUT)
                async with ClientSession(timeout=timeout) as session:
                    async with session.post(target_url, json=body) as resp:
                        # Prepare streaming response
                        response = web.StreamResponse(
                            status=resp.status,
                            headers={
                                "Content-Type": resp.content_type or "text/event-stream",
                                "Cache-Control": "no-cache",
                            }
                        )
                        await response.prepare(request)

                        # Stream response chunks
                        first_chunk = True
                        async for chunk in resp.content.iter_any():
                            if first_chunk:
                                ttft = time.time() - start_time
                                first_chunk = False
                            await response.write(chunk)
                            total_tokens += 1  # Approximate

                        await response.write_eof()

            except asyncio.TimeoutError:
                status = "timeout"
                error_msg = f"Request timed out after {REQUEST_TIMEOUT}s"
                if not self.shutting_down:
                    self.logger.error(f"[{request_id}] {error_msg}")
                return web.json_response(
                    {"error": {"message": error_msg, "type": "timeout_error"}},
                    status=504
                )

            except Exception as e:
                status = "error"
                error_msg = str(e)
                # Suppress error logging during shutdown to avoid noisy "Server disconnected" messages
                if not self.shutting_down:
                    self.logger.error(f"[{request_id}] Request failed: {e}")
                return web.json_response(
                    {"error": {"message": str(e), "type": "server_error"}},
                    status=502
                )

            # Log detailed metrics (skip during shutdown to avoid polluting benchmark data)
            if not self.shutting_down:
                total_latency = time.time() - start_time
                log_entry = {
                    "timestamp": start_time,
                    "request_id": request_id,
                    "prompt_hash": hash_prompt(prompt),
                    "prompt_length": len(prompt),
                    "user_location": {
                        "lat": loc["lat"],
                        "lon": loc["lon"],
                        "country": loc.get("country"),
                        "state": loc.get("state"),
                    },
                    "routed_to": node_name,
                    "node_ip": node_ip,
                    "distance_km": round(distance_km, 1),
                    "ttft_ms": round(ttft * 1000, 1) if ttft else None,
                    "total_latency_ms": round(total_latency * 1000, 1),
                    "status": status,
                    "error": error_msg,
                }

                # Write to file logger (JSONL format)
                file_handler = self.logger.handlers[0]  # File handler
                file_handler.stream.write(json.dumps(log_entry) + "\n")
                file_handler.stream.flush()

            return response

        finally:
            # Track request completion for graceful shutdown
            self.active_requests -= 1
            if self.shutting_down and self.active_requests == 0:
                self._shutdown_event.set()

    async def handle_models(self, request: web.Request) -> web.Response:
        """Handle /v1/models - forward to first available node."""
        for node in CLUSTER_NODES:
            try:
                url = f"http://{node[1]}:{node[2]}/v1/models"
                timeout = ClientTimeout(total=10)
                async with ClientSession(timeout=timeout) as session:
                    async with session.get(url) as resp:
                        data = await resp.json()
                        return web.json_response(data)
            except Exception:
                continue

        return web.json_response(
            {"error": {"message": "No nodes available", "type": "server_error"}},
            status=503
        )

    async def handle_health(self, request: web.Request) -> web.Response:
        """Health check endpoint."""
        return web.json_response({
            "status": "shutting_down" if self.shutting_down else "healthy",
            "request_count": self.request_count,
            "active_requests": self.active_requests,
            "node_stats": self.node_stats,
            "lookup_size": len(self.lookup),
        })

    async def graceful_shutdown(self) -> None:
        """Wait for in-flight requests to complete before shutdown."""
        self.shutting_down = True
        self.logger.info(f"Graceful shutdown initiated, {self.active_requests} requests in-flight")

        if self.active_requests > 0:
            self.logger.info(f"Waiting up to {SHUTDOWN_TIMEOUT}s for in-flight requests to complete...")
            try:
                await asyncio.wait_for(
                    self._shutdown_event.wait(),
                    timeout=SHUTDOWN_TIMEOUT
                )
                self.logger.info("All in-flight requests completed")
            except asyncio.TimeoutError:
                self.logger.warning(
                    f"Shutdown timeout reached with {self.active_requests} requests still in-flight"
                )
        else:
            self.logger.info("No in-flight requests, shutting down immediately")

    def create_app(self) -> web.Application:
        """Create the aiohttp application."""
        app = web.Application()
        app.router.add_post("/v1/chat/completions", self.handle_chat_completions)
        app.router.add_get("/v1/models", self.handle_models)
        app.router.add_get("/health", self.handle_health)

        # Register graceful shutdown handler
        app.on_shutdown.append(lambda _: self.graceful_shutdown())

        return app


# =============================================================================
# Main
# =============================================================================

def main():
    global SHUTDOWN_TIMEOUT

    parser = argparse.ArgumentParser(
        description="Geo-routing proxy for GuideLLM benchmarks"
    )
    parser.add_argument(
        "--port", type=int, default=9000,
        help="Port to listen on (default: 9000)"
    )
    parser.add_argument(
        "--lookup-file", type=str, default="wildchat_location_lookup.json",
        help="Path to location lookup JSON file"
    )
    parser.add_argument(
        "--log-file", type=str, default="geo_proxy_routing.log",
        help="Path to routing log file (JSONL format)"
    )
    parser.add_argument(
        "--shutdown-timeout", type=int, default=30,
        help="Max seconds to wait for in-flight requests during shutdown (default: 30)"
    )
    args = parser.parse_args()

    # Update global shutdown timeout
    SHUTDOWN_TIMEOUT = args.shutdown_timeout

    print("""
╔══════════════════════════════════════════════════════════════╗
║           GuideLLM Geo-Routing Proxy                         ║
╚══════════════════════════════════════════════════════════════╝
""")
    print("Cluster nodes:")
    for name, ip, port, lat, lon in CLUSTER_NODES:
        print(f"  • {name}: {ip}:{port} ({lat:.2f}, {lon:.2f})")
    print()

    proxy = GeoProxy(args.lookup_file, args.log_file)
    app = proxy.create_app()

    print(f"Starting proxy on http://localhost:{args.port}")
    print(f"Routing log: {args.log_file}")
    print(f"Graceful shutdown timeout: {SHUTDOWN_TIMEOUT}s")
    print()
    print("GuideLLM usage:")
    print(f"  guidellm benchmark --target http://localhost:{args.port}/v1 ...")
    print()

    # Run with graceful shutdown support
    # aiohttp's run_app handles SIGINT/SIGTERM and triggers on_shutdown handlers
    web.run_app(app, port=args.port, print=None, handle_signals=True)


if __name__ == "__main__":
    main()
