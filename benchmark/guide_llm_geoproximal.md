cd benchmark

# Install dependencies
pip install -r requirements.txt

# Option 1: Run everything with the convenience script
./run_guidellm_geo.sh --profile poisson --rate 5 --duration 60

# Option 2: Run steps manually
# 1. Preprocess WildChat (one-time)
python preprocess_wildchat_geo.py --num-samples 10000

# 2. Start the geo-proxy
python geo_proxy.py --port 9000 &

# 3. Run GuideLLM
guidellm benchmark \
  --target http://localhost:9000/v1 \
  --profile poisson --rate 5 \
  --max-seconds 60 \
  --data wildchat_guidellm.jsonl \
  --data-column-mapper '{"text_column": "prompt"}'



make sure geo_proxy.py hits port 8000 if u want requests to be forwarded to loadbalancers and to use a real policy. hit 8080 if u want it to go directly to the sglang server. 


# Switch all nodes to gorgo
gotoni lb policy gorgo --host 192.9.140.201,130.61.109.93,151.145.83.16

# Or via curl
curl "http://192.9.140.201:8000/lb/policy?set=gorgo"

# — 


python parse_guidellm_results.py /Users/abinayadinesh/Documents/projects/gotoni/benchmark/guidellm_results/benchmarks.json


# — 



