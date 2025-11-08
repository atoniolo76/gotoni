/**
 * Cloudflare Worker - Simple HTTP Proxy
 * Forwards all requests to TARGET_URL, preserving path and query string
 * 
 * IMPORTANT: Cloudflare Workers cannot access IP addresses directly.
 * You MUST use a domain name (not an IP address) for TARGET_URL.
 * 
 * Setup:
 * 1. Create an A record pointing to your server IP (api.alessiotoniolo.com → 129.146.109.44)
 * 2. Set DNS record to "DNS only" (gray cloud, not proxied)
 * 3. Wait for DNS propagation (usually 5-30 minutes, use check_dns.py to verify)
 * 4. Set TARGET_URL in Cloudflare Workers dashboard (optional, defaults to api.alessiotoniolo.com:8000):
 *    - Go to Workers & Pages > Your Worker > Settings > Variables
 *    - Add environment variable: TARGET_URL = http://api.alessiotoniolo.com:8000
 */

export default {
  async fetch(request, env) {
    // Remove trailing slash from TARGET_URL if present
    let TARGET_URL = (env.TARGET_URL || 'http://api.alessiotoniolo.com:8000').replace(/\/$/, '');
    
    // Parse target URL to extract host
    const targetUrlObj = new URL(TARGET_URL);
    const targetHost = targetUrlObj.host; // e.g., "129.146.109.44:8000"
    
    // Get the path and query from the incoming request
    const url = new URL(request.url);
    
    // Build target URL - ensure single slash between base and path
    const path = url.pathname.startsWith('/') ? url.pathname : '/' + url.pathname;
    const targetUrl = `${TARGET_URL}${path}${url.search}`;
    
    // Create new headers, filtering out problematic ones
    const headers = new Headers();
    for (const [key, value] of request.headers.entries()) {
      // Skip headers that Cloudflare Workers can't forward
      if (!['host', 'cf-connecting-ip', 'cf-ray', 'cf-visitor', 'cf-request-id'].includes(key.toLowerCase())) {
        headers.set(key, value);
      }
    }
    
    // Set Host header to the target host (this might help with IP access)
    headers.set('Host', targetHost);
    
    // Get request body (null for GET/HEAD requests)
    let body = null;
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      body = await request.clone().arrayBuffer();
    }
    
    // Forward the request with additional options
    try {
      const response = await fetch(targetUrl, {
        method: request.method,
        headers: headers,
        body: body,
        // Try to bypass Cloudflare's IP blocking by using these options
        redirect: 'follow',
      });
      
      // Check if we got blocked by Cloudflare
      if (response.status === 403) {
        const text = await response.text();
        if (text.includes('Direct IP access not allowed')) {
          return new Response(
            JSON.stringify({ 
              error: 'Cloudflare blocks direct IP access',
              message: 'Cloudflare Workers cannot access IP addresses directly. You need to either: 1) Use a domain name pointing to your IP, 2) Use Cloudflare Tunnel, or 3) Use a different proxy service.',
              targetUrl: targetUrl,
              suggestion: 'Set up a domain name or use Cloudflare Tunnel (cloudflared)'
            }),
            { 
              status: 502, 
              headers: { 
                'Content-Type': 'application/json',
                'Access-Control-Allow-Origin': '*'
              } 
            }
          );
        }
      }
      
      // Create response with CORS headers
      const responseHeaders = new Headers(response.headers);
      responseHeaders.set('Access-Control-Allow-Origin', '*');
      responseHeaders.set('Access-Control-Allow-Methods', 'GET, POST, PUT, DELETE, OPTIONS');
      responseHeaders.set('Access-Control-Allow-Headers', '*');
      
      return new Response(response.body, {
        status: response.status,
        statusText: response.statusText,
        headers: responseHeaders,
      });
    } catch (error) {
      return new Response(
        JSON.stringify({ 
          error: 'Proxy error', 
          message: error.message,
          targetUrl: targetUrl,
          note: 'If you see "Direct IP access not allowed", Cloudflare is blocking IP access. Use a domain name or Cloudflare Tunnel instead.'
        }),
        { 
          status: 502, 
          headers: { 
            'Content-Type': 'application/json',
            'Access-Control-Allow-Origin': '*'
          } 
        }
      );
    }
  },
};

