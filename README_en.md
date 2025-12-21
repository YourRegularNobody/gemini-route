# Gemini-Route

**Gemini-Route** is a high-performance, zero-dependency gateway for the Google Gemini API, written in Go.

Unlike standard reverse proxies that simply forward HTTP requests, Gemini-Route actively manages the network transport layer. It is designed to solve specific challenges encountered when running high-concurrency workloads or accessing Gemini from restricted network environments.

## Why Gemini-Route?

Most existing solutions (Node.js/Python wrappers) suffer from two main issues: heavy runtime dependencies and a lack of control over the outgoing TCP connection.

Gemini-Route addresses these by providing:

*   **Native IPv6 Rotation**: Utilizing a `/64` or `/48` IPv6 subnet to assign a unique source IP for outgoing connections. This effectively mitigates `429 Too Many Requests` errors by distributing load across thousands of IP addresses.
*   **Smart IPv6 Routing**: Bypasses standard DNS resolution (which often returns IPv4) by directly dialing verified Google IPv6 endpoints while maintaining correct SNI (Server Name Indication).
*   **Zero Dependency**: Distributed as a single, static binary (~10MB). No Node.js, NPM, or Python environment required.
*   **High Concurrency**: Built on Go's `net/http` with a tuned `Transport` layer. Supports connection pooling (Keep-Alive) to balance handshake latency against IP rotation.

## Key Features

*   **Source IP Randomization**: Automatically detects local IPv6 subnets and randomizes the source address for every new TCP connection.
*   **Destination Discovery**: Fetches and hot-reloads valid Google Gemini IPv6 endpoints from a maintained list (automatic failover to system DNS if the list is unreachable).
*   **Hot Reloading**: Updates target IP lists in the background without dropping active connections or interrupting stream generation.
*   **Privacy Aware**: Automatically sanitizes sensitive API keys (`key=...`) from server logs.
*   **Stream Optimized**: `FlushInterval` set to -1 to ensure real-time token streaming (SSE) without buffering.

## Architecture & Optimizations

### Connection Strategy
Gemini-Route does not disable Keep-Alive. Instead, it manages a connection pool (`MaxIdleConns: 2000`).
*   **High Load**: When concurrency exceeds the idle pool size, new connections are dialed using fresh source IPs from your subnet.
*   **Low Load**: Existing connections are reused to save TLS handshake time (approx. 50-100ms savings per request).
*   **Result**: Natural, session-based IP rotation that mimics valid user behavior rather than bot-like "one request per connection" patterns.

### IPv6 Direct Dialing
Standard DNS often resolves `generativelanguage.googleapis.com` to IPv4, rendering IPv6-only proxies useless. Gemini-Route forces `tcp6` dialing to specific Google infrastructure IPs while preserving the TLS hostname, ensuring connectivity even on IPv6-only VPS or HE.net tunnels.

## Usage

### 1. Quick Start
Download the binary and run. By default, it listens on `:8080`.

```bash
# If you have a configured IPv6 environment
./gemini-route
```

### 2. Configuration (Flags & Environment Variables)

Priority: Flag > Environment Variable > Default.

| Flag | Env Var | Default | Description |
| :--- | :--- | :--- | :--- |
| `--listen` | `LISTEN_ADDR` | `:8080` | Service listening address. |
| `--target` | `TARGET_HOST` | `generativelanguage.googleapis.com` | Upstream API host. |
| `--cidr` | `IPV6_CIDR` | *(Auto-detected)* | Manual source subnet (e.g., `2001:db8::/48`). |
| `--log-level`| `LOG_LEVEL` | `ERROR` | `DEBUG`, `INFO`, `WARN`, `ERROR`. |
| `--log-file` | `LOG_FILE` | *(None)* | Path to log file. Empty means stdout only. |

**Example:**
```bash
./gemini-route --listen :9090 --cidr 2001:db8:abcd::/48 --log-level INFO
```

### 3. Docker

```bash
docker run -d \
  --network host \
  --name gemini-route \
  -e IPV6_CIDR="2001:db8::/48" \
  -e LOG_LEVEL="INFO" \
  gemini-route:latest
```
*Note: `--network host` is recommended to allow the container to utilize the host's full IPv6 range.*

## Client Integration

Gemini-Route is fully compatible with the official Gemini API signature. Simply change the `Base URL` in your client SDK or request.

**Original:**
`https://generativelanguage.googleapis.com/v1beta/models/gemini-pro:generateContent?key=AIza...`

**With Gemini-Route:**
`http://your-server-ip:8080/v1beta/models/gemini-pro:generateContent?key=AIza...`

## Building from Source

```bash
git clone https://github.com/ccbkkb/gemini-route.git
cd gemini-route
go build -ldflags="-s -w" -o gemini-route main.go
```

## License

MIT License. See [LICENSE](LICENSE) for details.

This project is for technical research and educational purposes only. Please comply with Google's Terms of Service when using their API.
