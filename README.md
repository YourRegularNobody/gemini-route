# Gemini-Route

**Gemini-Route** 是一个高性能、零依赖的 Google Gemini API 网关，完全使用 Go 语言编写。

与简单的 HTTP 反向代理不同，Gemini-Route 在网络传输层进行了深度定制。它专门解决了在高并发场景、受限网络环境或需要大规模 API 调用时面临的连接管理与风控规避问题。

## 核心优势

现有的 Node.js 或 Python 封装方案通常存在运行时依赖重、内存占用高以及无法精确控制底层 TCP 连接等问题。Gemini-Route 针对性地解决了这些痛点：

*   **原生 IPv6 轮转**：利用 VPS 或隧道提供的 `/64` 或 `/48` IPv6 子网，为每个新建的 TCP 连接分配随机的源 IP 地址。这能将出口流量分散到数万个 IP 上，有效规避 `429 Too Many Requests` 限制。
*   **智能 IPv6 路由**：绕过通常只返回 IPv4 的标准 DNS 解析，强制使用 `tcp6` 直连经校验的 Google IPv6 节点，同时保持正确的 SNI (Server Name Indication) 握手。
*   **零依赖交付**：编译后为单文件（约 10MB），无需安装 Node.js、NPM 或 Python 环境，直接运行。
*   **高并发架构**：基于 Go 的 `net/http` 构建，配合调优后的 `Transport` 层。通过连接池策略在“降低握手延迟”与“IP 轮转风控”之间取得最佳平衡。

## 功能特性

*   **源地址随机化**：自动探测本地 IPv6 子网，并在拨号时随机绑定源 IP。
*   **目标地址发现**：自动拉取并热更新可用的 Google Gemini IPv6 端点列表（列表不可用时自动降级）。
*   **热重载 (Hot Reload)**：在后台静默更新目标 IP 列表，不中断任何活跃连接或正在进行的流式传输。
*   **隐私感知**：自动清洗服务端日志中的敏感 API Key (`key=...`)，仅保留路径信息。
*   **流式传输优化**：强制 `FlushInterval` 为 -1，确保 Token 生成的实时推送 (SSE)，无缓冲延迟。

## 架构与优化

### 连接池策略
Gemini-Route 不会简单粗暴地禁用 Keep-Alive，而是维护了一个高效的连接池 (`MaxIdleConns: 2000`)。
*   **高并发时**：当并发数超过空闲连接数，程序会发起新的拨号，此时会从子网中生成全新的源 IP。
*   **低负载时**：复用现有连接，节省昂贵的 TLS 握手开销（单次握手约节省 50-100ms）。
*   **效果**：实现了“基于会话的 IP 轮转”。这比机器式的“一请求一换 IP”更符合真实用户行为，且性能更优。

### IPv6 强制直连
标准环境下的 DNS 往往优先返回 IPv4 地址，导致 IPv6 代理池失效。Gemini-Route 实现了自定义的 `DialContext`，强制通过 IPv6 协议栈连接 Google 基础设施，确保流量完全走 IPv6 通道。

## 使用说明

### 1. 快速开始
下载二进制文件并运行。默认监听 `:8080` 端口。

```bash
# 确保机器具备 IPv6 环境
./gemini-route
```

### 2. 配置参数 (Flags & 环境变量)

优先级：命令行参数 > 环境变量 > 默认值。

| 参数 | 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--listen` | `LISTEN_ADDR` | `:8080` | 服务监听地址 |
| `--target` | `TARGET_HOST` | `generativelanguage.googleapis.com` | 上游 API 域名 |
| `--cidr` | `IPV6_CIDR` | *(自动探测)* | 手动指定源 IPv6 子网 (如 `2001:db8::/48`) |
| `--log-level`| `LOG_LEVEL` | `ERROR` | 日志等级: `DEBUG`, `INFO`, `WARN`, `ERROR` |
| `--log-file` | `LOG_FILE` | *(无)* | 日志文件路径，留空则仅输出到控制台 |

**示例：**
```bash
./gemini-route --listen :9090 --cidr 2001:db8:abcd::/48 --log-level INFO
```

### 3. Docker 部署

```bash
docker run -d \
  --network host \
  --name gemini-route \
  -e IPV6_CIDR="2001:db8::/48" \
  -e LOG_LEVEL="INFO" \
  gemini-route:latest
```
*注意：建议使用 `--network host` 模式，以便容器能直接使用宿主机的完整 IPv6 地址段。*

## 客户端集成

Gemini-Route 完全兼容官方 API 协议。仅需将客户端 SDK 或请求中的 `Base URL` 替换为本服务地址。

**原始地址：**
`https://generativelanguage.googleapis.com/v1beta/models/gemini-pro:generateContent?key=AIza...`

**代理地址：**
`http://your-server-ip:8080/v1beta/models/gemini-pro:generateContent?key=AIza...`

## 源码编译

```bash
git clone https://github.com/ccbkkb/gemini-route.git
cd gemini-route
go build -ldflags="-s -w" -o gemini-route main.go
```

## 许可证

MIT License. 详见 [LICENSE](LICENSE) 文件。

本项目仅供技术研究与教育目的使用。使用时请遵守 Google API 服务条款。
