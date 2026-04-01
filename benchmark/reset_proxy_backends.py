#!/usr/bin/env python3
"""Clear all servers from the gotoni proxy pool, then add SGLang backends (tunnel 8080) from sandbox_info.json.

The *proxy* control URL must be the Modal HTTPS tunnel for port 8000 (see launch_sandboxes.py output),
e.g. https://xxxx.r448.modal.host — not http://r448.modal.host:NNNN (that often TLS-mismatches and disconnects).

Override: PROXY=https://your-proxy-tunnel.modal.host python3 reset_proxy_backends.py
(Required when proxy-us-east in sandbox_info.json has no tunnels — e.g. launch failed with
"Sandbox with this name already exists". Use `modal shell` / Modal dashboard for the running
proxy sandbox tunnel URL, or terminate the old sandbox and re-run launch_sandboxes.py.)

TLS: pip install certifi, or GOTONI_PROXY_INSECURE_SSL=1, or rely on curl fallback (-k if needed).

If you see "Remote end closed connection", the tunnel URL is often stale: re-run
`python launch_sandboxes.py` and paste the new proxy-us-east :8000 URL into sandbox_info.json (or PROXY=).
"""

from __future__ import annotations

import http.client
import json
import os
import shutil
import ssl
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request


def _ssl_context(*, insecure: bool) -> ssl.SSLContext:
    if insecure:
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        return ctx
    try:
        import certifi  # type: ignore

        return ssl.create_default_context(cafile=certifi.where())
    except ImportError:
        return ssl.create_default_context()


def _is_cert_verify_err(err: BaseException) -> bool:
    if isinstance(err, urllib.error.URLError) and err.reason is not None:
        r = err.reason
        if isinstance(r, ssl.SSLError):
            return True
        msg = str(r).lower()
        if "certificate verify failed" in msg or "ssl" in msg and "verify" in msg:
            return True
    return False


def _urllib_should_retry(err: BaseException) -> bool:
    """Use curl when urllib gets an empty / dropped HTTP response."""
    if isinstance(err, urllib.error.URLError):
        r = err.reason
        if isinstance(r, http.client.RemoteDisconnected):
            return True
        msg = str(r).lower()
        if "remote end closed" in msg or "connection reset" in msg or "eof" in msg:
            return True
    if isinstance(err, ConnectionError):
        return True
    if isinstance(err, BrokenPipeError):
        return True
    return False


def _tunnel_base(tunnel: str) -> str:
    tunnel = tunnel.strip()
    if tunnel.startswith("http://") or tunnel.startswith("https://"):
        return tunnel.rstrip("/")
    return "https://" + tunnel.lstrip("/")


def _host_port(tunnel: str) -> tuple[str, int]:
    u = urllib.parse.urlparse(_tunnel_base(tunnel))
    if not u.hostname:
        raise ValueError(f"bad tunnel URL: {tunnel!r}")
    port = u.port or (443 if u.scheme == "https" else 80)
    return u.hostname, port


def _request_urllib(method: str, url: str, data: bytes | None = None) -> tuple[int, bytes]:
    insecure_env = os.environ.get("GOTONI_PROXY_INSECURE_SSL", "").lower() in ("1", "true", "yes")
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Connection", "close")
    req.add_header("User-Agent", "gotoni-reset-proxy/1.0")
    if data is not None:
        req.add_header("Content-Type", "application/json")

    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != "https":
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()

    attempts: tuple[bool, ...] = (True,) if insecure_env else (False, True)
    for insecure in attempts:
        ctx = _ssl_context(insecure=insecure)
        try:
            with urllib.request.urlopen(req, timeout=60, context=ctx) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()
        except urllib.error.URLError as e:
            if insecure or not _is_cert_verify_err(e):
                raise
            print(
                "SSL verify failed; retrying without certificate verification. "
                "pip install certifi helps. Or: GOTONI_PROXY_INSECURE_SSL=1",
                file=sys.stderr,
            )
            req = urllib.request.Request(url, data=data, method=method)
            req.add_header("Connection", "close")
            req.add_header("User-Agent", "gotoni-reset-proxy/1.0")
            if data is not None:
                req.add_header("Content-Type", "application/json")
    raise RuntimeError("reset_proxy_backends: HTTPS request failed after retries")


def _request_curl(method: str, url: str, data: bytes | None = None) -> tuple[int, bytes]:
    if not shutil.which("curl"):
        raise OSError("curl not in PATH")
    insecure = os.environ.get("GOTONI_PROXY_INSECURE_SSL", "").lower() in ("1", "true", "yes")
    with tempfile.NamedTemporaryFile(delete=False, suffix=".body") as tmp:
        body_path = tmp.name
    try:
        cmd: list[str] = [
            "curl",
            "-sS",
            "--http1.1",
            "--max-time",
            "60",
            "-o",
            body_path,
            "-w",
            "%{http_code}",
            "-X",
            method,
            "-H",
            "Connection: close",
            "-H",
            "User-Agent: gotoni-reset-proxy/1.0 (curl)",
        ]
        if insecure:
            cmd.append("-k")
        if data is not None:
            cmd.extend(["-H", "Content-Type: application/json", "-d", data.decode()])
        cmd.append(url)
        proc = subprocess.run(cmd, capture_output=True, timeout=90)
        if proc.returncode != 0:
            err = (proc.stderr or proc.stdout or b"").decode()
            raise urllib.error.URLError(f"curl exit {proc.returncode}: {err}")
        code_s = proc.stdout.decode().strip()
        if not code_s.isdigit():
            raise urllib.error.URLError(f"curl bad -w output: {code_s!r}")
        status = int(code_s)
        with open(body_path, "rb") as f:
            body = f.read()
        return status, body
    finally:
        try:
            os.unlink(body_path)
        except OSError:
            pass


def _request(method: str, url: str, data: bytes | None = None) -> tuple[int, bytes]:
    try:
        return _request_urllib(method, url, data)
    except (urllib.error.URLError, http.client.RemoteDisconnected, OSError, ConnectionError, BrokenPipeError) as e:
        if not _urllib_should_retry(e):
            raise
        print("urllib failed; retrying same request with curl --http1.1 ...", file=sys.stderr)
        try:
            return _request_curl(method, url, data)
        except Exception as curl_err:
            print(f"curl fallback also failed: {curl_err}", file=sys.stderr)
            raise e from curl_err


def _proxy_tunnel_8000(rows: list) -> str | None:
    for r in rows:
        if r.get("name") != "proxy-us-east":
            continue
        t = r.get("tunnels") or {}
        u = t.get("8000")
        return u if u else None
    return None


def main() -> None:
    here = os.path.dirname(os.path.abspath(__file__))
    path = os.path.join(here, "sandbox_info.json")
    with open(path) as f:
        rows = json.load(f)

    proxy_base = os.environ.get("PROXY", "").strip()
    if proxy_base:
        proxy_base = _tunnel_base(proxy_base).rstrip("/")
    else:
        u = _proxy_tunnel_8000(rows)
        if not u:
            print(
                "No proxy tunnel URL: set PROXY=https://<host> (Modal tunnel for gotoni proxy port 8000), "
                "or fix sandbox_info.json so proxy-us-east has tunnels.8000.\n"
                "If launch failed with 'Sandbox with this name already exists', delete/rename the old "
                "sandbox in Modal or set PROXY= to the running proxy's :8000 tunnel URL.",
                file=sys.stderr,
            )
            sys.exit(1)
        proxy_base = _tunnel_base(u).rstrip("/")

    if proxy_base.startswith("http://") and ".modal.host" in proxy_base:
        print(
            "Hint: Modal proxy tunnels usually need https:// (not http://). "
            "Re-run `python launch_sandboxes.py` for fresh URLs.",
            file=sys.stderr,
        )

    list_url = f"{proxy_base}/proxy/servers"

    try:
        status, raw = _request("GET", list_url)
        if status >= 400:
            print(f"GET {list_url!r} -> HTTP {status}: {raw[:500]!r}", file=sys.stderr)
            sys.exit(1)
        servers = json.loads(raw.decode())
    except (urllib.error.URLError, json.JSONDecodeError, OSError) as e:
        print(f"Failed to list servers at {list_url!r}: {e}", file=sys.stderr)
        print(
            "Check: (1) gotoni proxy running in proxy-us-east sandbox, "
            "(2) tunnel URL still valid — run `python launch_sandboxes.py` and update sandbox_info.json or PROXY=.",
            file=sys.stderr,
        )
        sys.exit(1)

    for s in servers:
        sid = s.get("id") or f'{s["ip"]}:{s["port"]}'
        q = urllib.parse.urlencode({"id": sid})
        del_url = f"{proxy_base}/proxy/servers?{q}"
        try:
            st, _ = _request("DELETE", del_url)
            if st >= 400:
                print(f"remove {sid}: HTTP {st}", file=sys.stderr)
            else:
                print(f"removed {sid}")
        except urllib.error.HTTPError as e:
            print(f"remove {sid}: {e.code} {e.reason}", file=sys.stderr)

    for r in rows:
        if r.get("role") != "sglang":
            continue
        t = (r.get("tunnels") or {}).get("8080")
        if not t:
            print(f"skip {r.get('name', '?')}: no tunnels.8080 in sandbox_info.json", file=sys.stderr)
            continue
        host, port = _host_port(t)
        body = json.dumps({"id": f"{host}:{port}", "ip": host, "port": port}).encode()
        try:
            st, _ = _request("POST", f"{proxy_base}/proxy/servers", data=body)
            if st >= 400:
                print(f"add {r['name']}: HTTP {st}", file=sys.stderr)
                sys.exit(1)
            print(f"added {r['name']} -> {host}:{port}")
        except urllib.error.HTTPError as e:
            print(f"add {r['name']}: {e.code} {e.reason}", file=sys.stderr)
            sys.exit(1)


if __name__ == "__main__":
    main()
