# Modal setup
```
cd benchmark && python launch_sandboxes.py
```

```
cd ..
go build -trimpath -ldflags="-s -w" -o gotoni .
./gotoni proxy deploy proxy-us-east
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

GuideLLM benchmark (through the proxy OpenAI endpoint):
```
pip install -r requirements.txt
export PROXY_TARGET="$(jq -r '.[] | select(.name=="proxy-us-east") | .tunnels["8000"]' sandbox_info.json)/v1"
./run_guidellm_proxy.sh
```
Optional env: `GUIDELLM_DATA_SAMPLES`, `GUIDELLM_MAX_SECONDS`, `GUIDELLM_RATE`, `GUIDELLM_MODEL` (must match SGLang `--model-path`). If `sandbox_info.json` is stale, set `PROXY_TARGET` manually to the proxy :8000 tunnel URL plus `/v1`.