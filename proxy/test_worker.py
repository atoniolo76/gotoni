#!/usr/bin/env python3
"""
Test script for Cloudflare Worker proxy
Tests GET endpoints and POST endpoint
"""

import requests
import base64

# Your Cloudflare Worker URL (NO PORT NUMBER!)
WORKER_URL = "https://xanderski.atoniolo76.workers.dev"

print("=" * 60)
print("Testing Cloudflare Worker Proxy")
print("=" * 60)

# Test 1: GET base URL (should hit http://129.146.109.44:8000/)
print("\n1. Testing GET / (base URL)...")
try:
    response = requests.get(f"{WORKER_URL}/", timeout=10)
    print(f"   Status: {response.status_code}")
    print(f"   Response: {response.text[:200]}...")
except Exception as e:
    print(f"   Error: {e}")

# Test 2: GET /health endpoint
print("\n2. Testing GET /health...")
try:
    response = requests.get(f"{WORKER_URL}/health", timeout=10)
    print(f"   Status: {response.status_code}")
    print(f"   Response: {response.json()}")
except Exception as e:
    print(f"   Error: {e}")

# Test 3: GET /align endpoint (might return 405 Method Not Allowed, which is fine)
print("\n3. Testing GET /align...")
try:
    response = requests.get(f"{WORKER_URL}/align", timeout=10)
    print(f"   Status: {response.status_code}")
    print(f"   Response: {response.text[:200]}...")
except Exception as e:
    print(f"   Error: {e}")

# Test 4: POST /align with test data
print("\n4. Testing POST /align...")
try:
    # Create a minimal test audio (just a small base64 string)
    test_audio_base64 = base64.b64encode(b"fake audio data").decode('utf-8')
    
    response = requests.post(
        f"{WORKER_URL}/align",
        json={
            "text": "The quick brown fox jumps over the lazy dog",
            "audio_data": test_audio_base64,
            "language": "eng",
            "batch_size": 16
        },
        timeout=30
    )
    print(f"   Status: {response.status_code}")
    print(f"   Response: {response.text[:500]}...")
    
    # Try to parse as JSON
    try:
        print(f"   JSON: {response.json()}")
    except:
        print("   (Response is not valid JSON)")
        
except Exception as e:
    print(f"   Error: {e}")

print("\n" + "=" * 60)
print("Tests complete!")
print("=" * 60)

