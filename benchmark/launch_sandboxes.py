"""Launch Modal sandboxes for the proxy benchmark.

- **proxy** (CPU): lightweight image, `sleep infinity` — deploy gotoni with `gotoni proxy deploy`.
- **sglang** (GPU): `lmsysorg/sglang:latest`, runs `sglang.launch_server` with `--enable-metrics` so the
  gotoni proxy can poll `/metrics` (required for routing / health).

Optional: set `HF_TOKEN` in the environment or attach a Modal Secret for Hugging Face model download.
"""

import json
import os

import modal

ENV_NAME = "alessio-dev"
APP_NAME = "gotoni-benchmark"

# Same family as pkg/config/constants.go DefaultSGLangDockerImage — includes CUDA + SGLang.
SGLANG_IMAGE = modal.Image.from_registry("lmsysorg/sglang:latest")

# CPU proxy node does not need the full SGLang image.
PROXY_IMAGE = modal.Image.from_registry("ubuntu:22.04")

SGLANG_LISTEN_PORT = 8080

# Fits a single A100; same model id as pkg/tokenizer/main.rs (Mistral-7B-Instruct-v0.3).
# (pkg/config DefaultSGLangModel is a much larger Mistral—use that only when GPUs allow.)
SGLANG_MODEL = "mistralai/Mistral-7B-Instruct-v0.3"

# lmsysorg/sglang image can miss protobuf; sentencepiece then fails with
# ModuleNotFoundError: No module named 'google' (needs google.protobuf).
SGLANG_PIP_FIX = "python -m pip install -q 'protobuf>=3.20'"

# --enable-metrics exposes Prometheus metrics on /metrics (gotoni proxy polls this).
SGLANG_LAUNCH_CMD = (
    f"{SGLANG_PIP_FIX} && "
    f"python -m sglang.launch_server "
    f"--model-path {SGLANG_MODEL} "
    f"--port {SGLANG_LISTEN_PORT} "
    f"--host 0.0.0.0 "
    f"--enable-metrics"
)

SANDBOXES = [
    {"name": "proxy-us-east", "region": "us-east", "gpu": None, "cpu": 4.0, "memory": 8192, "role": "proxy"},
    {"name": "sglang-us-west", "region": "us-west", "gpu": "A100", "cpu": 8.0, "memory": 32768, "role": "sglang"},
    {"name": "sglang-eu", "region": "eu", "gpu": "A100", "cpu": 8.0, "memory": 32768, "role": "sglang"},
    {"name": "sglang-ap", "region": "ap", "gpu": "A100", "cpu": 8.0, "memory": 32768, "role": "sglang"},
]


def _secrets_for_sglang():
    """Optional Hugging Face token from Modal secret (name configurable via MODAL_HF_SECRET_NAME)."""
    name = os.environ.get("MODAL_HF_SECRET_NAME", "").strip()
    if not name:
        return []
    try:
        return [modal.Secret.from_name(name)]
    except Exception:
        return []


def main():
    app = modal.App.lookup(APP_NAME, create_if_missing=True, environment_name=ENV_NAME)
    hf_secrets = _secrets_for_sglang()
    results = []

    for spec in SANDBOXES:
        print(f"\n{spec['name']} ({spec['region']})...")
        role = spec["role"]
        try:
            if role == "proxy":
                sb = modal.Sandbox.create(
                    "sleep",
                    "infinity",
                    app=app,
                    name=spec["name"],
                    image=PROXY_IMAGE,
                    gpu=spec["gpu"],
                    cpu=spec["cpu"],
                    memory=spec["memory"],
                    region=spec["region"],
                    timeout=3600 * 6,
                    unencrypted_ports=[8000, 8080],
                )
            elif role == "sglang":
                sglang_kwargs = dict(
                    app=app,
                    name=spec["name"],
                    image=SGLANG_IMAGE,
                    gpu=spec["gpu"],
                    cpu=spec["cpu"],
                    memory=spec["memory"],
                    region=spec["region"],
                    timeout=3600 * 6,
                    unencrypted_ports=[SGLANG_LISTEN_PORT],
                )
                if hf_secrets:
                    sglang_kwargs["secrets"] = hf_secrets
                sb = modal.Sandbox.create(
                    "/bin/bash",
                    "-c",
                    SGLANG_LAUNCH_CMD,
                    **sglang_kwargs,
                )
            else:
                raise ValueError(f"unknown role {role!r}")

            print(f"  {sb.object_id}")
            results.append({
                "name": spec["name"],
                "sandbox_id": sb.object_id,
                "region": spec["region"],
                "role": spec["role"],
                "gpu": spec["gpu"],
            })
        except Exception as e:
            print(f"  FAILED: {e}")
            results.append({"name": spec["name"], "region": spec["region"], "role": spec["role"], "error": str(e)})

    print("\n---")
    for r in results:
        print(r.get("sandbox_id", r.get("error", "?")), r["name"])

    print("\nTunnels (60s timeout)...")
    for r in results:
        if "sandbox_id" not in r:
            continue
        sb = modal.Sandbox.from_id(r["sandbox_id"])
        try:
            tunnels = sb.tunnels(timeout=60)
            r["tunnels"] = {p: str(t.url) for p, t in tunnels.items()}
            for p, u in r["tunnels"].items():
                print(f"  {r['name']}:{p} {u}")
        except Exception as e:
            print(f"  {r['name']}: {e}")

    out = "sandbox_info.json"
    with open(out, "w") as f:
        json.dump(results, f, indent=2)
    print(f"\nWrote {out}")


if __name__ == "__main__":
    main()
