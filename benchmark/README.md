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
./gotoni proxy servers list --host "$PROXY_HOST" --port "$PROXY_PORT" proxy status --host "$PROXY_HOST" --port "$PROXY_PORT"
```