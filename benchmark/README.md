# Modal setup
```
cd benchmark && python launch_sandboxes.py
```

```
cd ..
go build -trimpath -ldflags="-s -w" -o gotoni .
GOTONI_PROXY_TLS_INSECURE=1 ./gotoni proxy deploy proxy-us-east
```
Check the status of proxy deploy cmd:
```
export PROXY_HOST=r442.modal.host
export PROXY_PORT=37701

./gotoni proxy status --host "$PROXY_HOST" --port "$PROXY_PORT"
```

Wait a couple minutes for the sglang models to load

Reset backends before adding sglang servesr to proxy:
```
cd benchmark
python3 reset_proxy_backends.py
```

Check status of servers in proxy:
```
./gotoni proxy servers list --host "$PROXY_HOST" --port "$PROXY_PORT"
./gotoni proxy status --host "$PROXY_HOST" --port "$PROXY_PORT"
```

Switch the policy on-the-fly:
```
./gotoni proxy policy --host "r449.modal.host" --port "42721" 
```

GuideLLM benchmark (through the proxy OpenAI endpoint):
```
pip install -r requirements.txt
export PROXY_TARGET="$(jq -r '.[] | select(.name=="proxy-us-east") | .tunnels["8000"]' sandbox_info.json)/v1"
./run_guidellm_proxy.sh
```
**Default autoresearch:** runs **gorgo → least-load → prefix-tree**; **POST `/proxy/cache/clear`** before/after each GuideLLM run (proxy prefix-routing cache, not SGLang GPU KV); **60s** max, **25** concurrent workers, **`GUIDELLM_RANDOM_SEED=42`**, **`wildchat_guidellm.jsonl`** with **`GUIDELLM_DATA_SAMPLES=-1`** (all rows). Override with `GUIDELLM_MAX_SECONDS`, `GUIDELLM_RATE`, `GUIDELLM_RANDOM_SEED`, `GUIDELLM_POLICIES`. **Single run** (no policy sweep): `GUIDELLM_AUTORESEARCH=0 ./run_guidellm_proxy.sh`.

Optional env: `GUIDELLM_MODEL` (must match SGLang `--model-path`), `CURL_EXTRA` (e.g. TLS for proxy control curls). If `sandbox_info.json` is stale, set `PROXY_TARGET` manually to the proxy :8000 tunnel URL plus `/v1`.