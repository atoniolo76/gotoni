# GORGO: Maximizing KV-Cache while Minimizing Network Latency in Cross-Region LLM Inference

## Abstract (slightly old)
Distributing inference across geographical regions can improve TTFT by regionalizing service deployments. While existing multi-region load balancers improve uptime by prioritizing Key-Value cache overlap, they ignore cluster networking latency. In this work, we introduce GORGO, a novel method to minimize TTFT by optimizing the interplay between regional compute and global network overhead. Our approach is grounded in the insight that the total serving cost of a request is a function of available compute, network latency, and prefix caching. We benchmark GORGO against three baselines: (1) naive least-load routing, which ignores prefix-cache overlap; (2) prefix-similarity routing, which selectively pushes requests to the replica with the highest cached-prefix overlap; and (3) a prefix-aware HTTP proxy that centrally tracks prefix-cache state across all instances to route requests accordingly. Using extensive profiling and a latency-waterfall analysis, we treat coordination overhead as a first-class bottleneck and quantify how it contributes to TTFT across competing load-balancing policies. We show that GORGO optimizes network latency and maximizes KV-Cache Reuse across regions, providing a more responsive and cost-effective system for distributed LLM inference.

-- brainstorming
vertebrae:
1. Linear regression analyzing the effect of prefill tokens on TTFT
2. Real-time metrics collection

Baseline: 
1. least-load routing
2. 