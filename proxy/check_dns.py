#!/usr/bin/env python3
"""
Check if DNS has propagated for your domain
"""

import socket
import sys
import time

def check_dns(domain, expected_ip):
    """Check if domain resolves to expected IP"""
    try:
        ip = socket.gethostbyname(domain)
        if ip == expected_ip:
            print(f"✓ DNS propagated! {domain} → {ip}")
            return True
        else:
            print(f"✗ DNS points to wrong IP: {domain} → {ip} (expected {expected_ip})")
            return False
    except socket.gaierror as e:
        print(f"✗ DNS not ready yet: {domain} - {e}")
        return False

if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("Usage: python3 check_dns.py <domain> <expected-ip>")
        print("Example: python3 check_dns.py api.example.com 129.146.109.44")
        sys.exit(1)
    
    domain = sys.argv[1]
    expected_ip = sys.argv[2]
    
    print(f"Checking DNS propagation for {domain}...")
    print(f"Expected IP: {expected_ip}")
    print("-" * 60)
    
    max_attempts = 60  # Check for up to 60 minutes
    check_interval = 60  # Check every 60 seconds
    
    for attempt in range(max_attempts):
        if check_dns(domain, expected_ip):
            print(f"\n✓ DNS is ready! You can now update your Cloudflare Worker.")
            print(f"Update TARGET_URL to: http://{domain}:8000")
            sys.exit(0)
        
        if attempt < max_attempts - 1:
            print(f"Waiting {check_interval} seconds before next check...")
            time.sleep(check_interval)
    
    print(f"\n✗ DNS did not propagate after {max_attempts * check_interval / 60} minutes.")
    print("This is unusual - check your DNS settings.")

