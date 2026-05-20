# mosproxy

源码: <https://github.com/IrineSistiana/mosproxy>

License: [GNU General Public License v3.0](https://github.com/IrineSistiana/mosproxy/blob/dev/LICENSE)

Docker image: <https://github.com/IrineSistiana/mosproxy/pkgs/container/mosproxy>

## 简介

一个 DNS 转发工具/框架。

* 支持主流 DNS 协议。(UDP/TCP/TLS/HTTPS/HTTP3/QUIC)
* 支持转发/复写/缓存 ECS (EDNS0 Client Subnet)。
* 支持上游负载均衡 (QPS 限制，健康检查，自动 fallback)。
* 可根据请求的 域名/HTTP Path/SNI/来源 IP 决定转发目的地。
* 支持基于响应 IP 的规则匹配，可对"被污染"的响应自动回退到备用上游。
* 支持上游域名的 Bootstrap DNS 解析，无需依赖系统 DNS。
* 提供 middleware 插件接口可扩展转发逻辑。
* 支持双向 TLS 验证 (mTLS)。
* 内置 prometheus metrics 统计。

性能优化:

* 网络 IO
  * 所有出站协议会尽可能的连接/socket复用。
  * [fasthttp](https://github.com/valyala/fasthttp) 实现的高性能 HTTP/1 服务端。适合反向代理。
* 高效的域名和 IP 匹配器。规则会优化成一个二进制索引，O(log(n)) 匹配复杂度，瞬间(百纳秒级别)完成匹配。0 碎片内存，0 GC压力，更少内存占用，以 100w 条规则为例，heap object 数量 300w -> 1。
* 缓存:
  * [otter](https://github.com/maypok86/otter) 实现的内存缓存。
  * [rueidis](https://github.com/redis/rueidis) 实现的 redis 缓存。
  * 内存和 redis 缓存可同时启用，组成多级缓存。一级内存，二级 redis。相比单 redis 缓存，因为如果一级内存缓存命中则无需再请求 redis，能大幅降低 mosproxy (约 30%) 和 redis (约 95%) 负载。
  * 支持基于物理地域的缓存 (geo based cache)。可以让相同物理地域(比如，相同省市和运营商)的客户共享缓存。相比传统按照 ECS 网段缓存方案，可将一个地区碎片的 IP 段聚合为一个缓存，提高缓存效率。
  * 缓存数据使用 [S2 压缩](https://github.com/klauspost/compress/tree/master/s2#s2-compression)。降低内存占用和约 90% heap object 数量 (n 条缓存 heap object 数量 ~20n -> 2n)。
* 1vCPU 可承受约 5w/qps 请求量。

## 命令

`mosproxy router -c config.yaml`

### proxy

如果仅需要简单的将所有请求转发至上游，无需复杂的路由逻辑，可以使用 proxy 子命令在命令行直接启动一个转发服务，无需配置文件。

```bash
# 示例: 转发至 Google DoH
mosproxy proxy -l 0.0.0.0:5353 -u https://dns.google/dns-query

# 更复杂的示例: 转发至 8.8.8.8，如果不可用，自动使用 1.1.1.1 做 fallback，
# 缓存大小 1M，转发 ECS。
mosproxy proxy -l 0.0.0.0:5353 --cache 1048576 -u 8.8.8.8 -u 1.1.1.1 --upstream-lb fall_through --ecs
```

<details>
<summary>proxy 子命令帮助 (可能不是最新，以 <code>mosproxy proxy -h</code> 命令输出为准)</summary>

```
Start a dns proxy service via command line. e.g:
Proxy from 0.0.0.0:5353 to Google DoH.
        proxy -l 0.0.0.0:5353 -u https://dns.google/dns-query
Proxy to Cloudflare DNS, send client subnet to upstreams, and use Google DNS as fallback.
        proxy -l 0.0.0.0:5353 -u 1.1.1.1 -u 8.8.8.8 --upstream-lb fall_through --ecs

Usage:
   proxy -l listen_addr -u upstream_addr [-u upstream_addr]... [flags]

Flags:
  -l, --listen string              Listening address.
  -p, --protocol string            Listening protocol.
                                   One of [udp|tcp|tls|http|fasthttp|https|quic].
                                   Default is listening both udp and tcp.
      --tls-cert string            Path to a file of PEM certificate.
      --tls-key string             Path to a file of PEM private key.
      --http-path string           Http path of DoH server.
  -u, --upstream strings           Upstream addr (can be specified multiple times).
                                   Format: [protocol://]host[:port][/path][?arg_key=arg_value][&arg_key=arg_value]...
                                   protocol can be one of: [udp|tcp[+pipeline]|tls[+pipeline]|https|h3|quic]
                                   e.g.: tcp://8.8.8.8:53, https://dns.google/dns-query?dial_addr=8.8.8.8&tag=google_doh
                                   Supported arg_key:
                                        tag: Tag name for this upstream, useful for logging.
                                        dial_addr: Specifying the IP address of the host.
                                                (Eliminate the need for host name resolution and establish connections faster)
                                        weight: Weight of this upstream. (See --upstream-lb flag)
                                        qps: QPS limit of this upstream. (See --upstream-lb flag, fall_through mode)
                                        max_fails: If the upstream failed max_fails times continuously, mark it as offline. (default 5)
                                                (See --upstream-lb flag)
                                        insecure: [true] Disable TLS server verification.
      --upstream-lb string         Upstream load balance mode:
                                        random: Use random online upstream.
                                        fall_through: Fallback to next online upstream if previous one was offline or reached QPS limit.
                                                Note: Upstream weights are ignored in this mode.
                                        qname_hash: Queries with same domain name will use the same online upstream.
                                        client_ip_hash: Queries from same ip will use the same online upstream. (default "random")
      --ecs                        Send EDNS0 Client Subnet (ECS) to upstream.
      --ecs-ip-zone string         Path to ecs ip zone file.
      --cache int                  Memory cache size in bytes (0: disable memory cache). (default 4194304)
      --cache-redis string         Redis cache backend address.
                                   If memory and redis cache are both enabled, redis will work as a second level cache.
                                   Format:
                                        redis://<user>:<password>@<host>:<port>/<db_number>?addr=<host2>:<port2>&addr=<host3>:<port3>
                                        unix://<user>:<password>@</path/to/redis.sock>?db=<db_number>
      --cache-optimistic-ttl int   If > 0, enable optimistic cache.
      --access-log                 Print all queries to log.
      --api string                 API server listening address.
  -h, --help                       help for proxy
```

</details>

## 配置

```yaml
log:
  # 日志文件路径。如果配置了该项，日志将输出到指定文件而非 stderr。
  # 文件以追加模式打开，输出格式为无颜色的人类可读格式。
  # 适合配合外部工具 (如 logrotate) 进行日志轮转。
  file: ""
  # 是否打印请求信息。(请求的来源，域名，类型，应答 rcode，延时等...)
  queries: false
  # 是否打印**收到和发送的所有** DNS 数据包。会打印大量信息。调试用。
  trace_msgs: false

api:
  addr: "127.0.0.1:8888" # api 监听地址。

servers: # 入站
    # (必需) 协议，支持: udp/tcp/tls/http/fasthttp/https/http3/quic
    #  http: go 标准库实现的 http/1，h2c 服务端。
    #  fasthttp: fasthttp 实现的 http/1 服务端。
  - protocol: "udp"
    # (必需) 监听地址。基于 TCP 的协议支持用 "@" 开头监听 abstract unix socket。
    listen: "127.0.0.1:5353"
    idle_timeout: 0 # 空连接超时。单位s。默认 tcp/tls 10，其他 30。一般不需要改。
    udp: # udp 相关设置
      # 仅 linux。
      # 如果服务器有多个 IP 地址并且没有指定监听地址时 (服务器客户端之间存在多个路由可能),
      # 启用该选项可让 linux 内核发送应答时，按照请求的来源路径原路返回。
      multi_routes: false
    tcp: # tcp 相关设置。(tcp/tls)
      # 单条连接最大并发处理请求数。默认 100。超出的请求会排队。
      max_concurrent_queries: 100
    tls: # tls 设置。(tls/https/http3/quic)
      cert: "" # pem 证书。
      key: "" # pem 私钥。
      ca: "" # pem ca 证书。
      insecure_skip_verify: false # 客户端禁用服务器证书验证。(服务端无用)
      verify_client_cert: false # 服务端启用客户端证书验证。(mTLS)
    http: # http 设置。(http/fasthttp/http3/https)
      path: "" # 路径。为空不会检查请求 url 路径。
      client_addr_header: "X-Forwarded-For" # 从这个 header 获取客户端 IP。
    quic: # quic 设置。(http3/quic)
      max_streams: 100 # 单个 quic 连接最大并发请求数。默认 100。
    socket: # linux so 设置。
      so_reuseport: false
      so_rcvbuf: 0
      so_sndbuf: 0
      so_mark: 0
      so_bindtodevice: ""

cache: # 缓存。
  # 内存大小，单位 byte。
  # 默认: 0 = 无内存缓存。
  mem_size: 1048576
  # redis 缓存。
  # redis://<user>:<password>@<host>:<port>/<db_number>
  # redis://<user>:<password>@<host>:<port>?addr=<host2>:<port2>&addr=<host3>:<port3>
  # unix://<user>:<password>@</path/to/redis.sock>?db=<db_number>
  # 如果和内存缓存同时启用，则 redis 为二级缓存。先查找内存缓存，如果没有命中，
  # 则查找 redis，如果 redis 缓存命中，则会更新内存缓存。
  # 自带 health (ping) check，如果 redis 服务端失联会自动禁用 redis 缓存，
  # 直到重新连接。检查间隔/反应时间为 1s。
  redis: ""
  # 最大 ttl。默认 10min。
  maximum_ttl: 600
  # 预取阈值。(0~1)。当 缓存的当前TTL < threshold*原TTL 会触发*被动预取*。
  # 被动预取: 当缓存命中时才会检查 TTL 并决定是否预取。不产生额外流量和负载。
  # 默认 0.25
  prefetch_threshold: 0.25
  # 乐观缓存 ttl。
  # 即使应答缓存过期，仍在继续保留 optimistic_ttl 秒，后续请求会返回过期应答，
  # 但会立即触发预取。
  # 默认 0 无乐观缓存。
  optimistic_ttl: 0

# ECS 设置
# ECS 来源优先级: 客户端 IP < 请求自带 ECS < overwrite
# ECS 处理逻辑:
#   1. 解析 客户端 IP 或 "请求自带 ECS"，作为 "即将转发至上游的 ECS"
#   2. "即将转发至上游的 ECS" 匹配 zone，确定请求所属 zone
#   3. zone 匹配 overwrite，复写 "即将转发至上游的 ECS"
ecs:
  enabled: false # 启用 ECS 支持。将发送 ECS 至上游。默认发送客户端 IP。
  forward: false # 是否转发请求自带的 ECS。
  ip_zone: ""        # ip zone 文件路径。启用基于 zone 的缓存。
  zone_overwrite: "" # zone overwrite 文件路径。

upstreams: # 出站。
  - tag: "google" # (必需)标签，需唯一。
    # (必需)地址 [protocol://]host[:port][/path]
    # protocol 为 udp/tcp[+pipeline]/tls[+pipeline]/https/h3/quic
    # 提示: pipeline 是 RFC 7766 定义的新的基于 TCP 的传输模式，连接利用率更高，但兼容性差，
    #   **必需**确定服务器支持才能使用。如不确定，建议考虑更现代的 DoH/QUIC 等协议。
    # e.g.:
    #   "8.8.8.8"
    #   "tls://8.8.8.8"
    #   "h3://dns.google/dns-query"
    addr: "https://dns.google/dns-query"
    # 手动指定 IP 协议层面建立连接时使用的地址。端口号可选。
    # 一般用于手动指定 addr 里域名地址的 IP。建立连接不再需要每次解析 addr 域名，
    # 能更快连接到上游。
    # 注意: 不能与 bootstrap 同时使用。
    dial_addr: "8.8.8.8"
    # Bootstrap DNS 解析器。值为另一个 upstream 的 tag。
    # 当 addr 为域名时，使用指定的 upstream 解析该域名的 IP，替代系统 DNS 解析。
    # 解析结果会缓存，缓存 TTL 取自 DNS 应答，最低 30 秒，默认(无记录时) 5 分钟。
    # 要求:
    #   - bootstrap 引用的 upstream 必须在当前 upstream 之前定义。
    #   - bootstrap 引用的 upstream 的 addr 必须是 IP 地址，或已配置 dial_addr。
    #   - 不能与 dial_addr 同时使用。
    #   - addr 本身为 IP 地址时不允许配置 bootstrap。
    bootstrap: ""
    # 是否禁止向该上游发送 ECS。启用后，发往该上游的请求不会附带 ECS 信息，
    # 即使全局 ECS 已启用。经过负载均衡器时，如果选中的后端启用了 no_ecs，
    # 同样会移除 ECS。
    no_ecs: false
    tls: # tls 设置。见上。
      cert: ""
      key: ""
      ca: ""
      insecure_skip_verify: false
      verify_client_cert: false # (客户端无用)
    socket: # linux so 设置。
      so_reuseport: false
      so_rcvbuf: 0
      so_sndbuf: 0
      so_mark: 0
      so_bindtodevice: ""
    health_check: # health check 设定。另见下文 负载均衡。
      # 如果 max_fails > 0，启动 health check。
      # 如果上游连续失败 max_fails 次，标记为 offline。
      # 被标记 offline 的上游将不再转发任何请求。
      # 默认 0。如需 health check，建议值 5~10。
      max_fails: 0
      # ping 测试(最大)间隔。单位 s。默认 120。
      # 上游 offline 后，会进行 ping 测试。ping 成功后上游将重新标记为 online。
      # 如果上游无请求 (使用负载均衡将请求 fallback 至其他上游)，ping 间隔会逐渐
      # 递增: 1,2,4,...,2^n,ping_interval。
      # 如果上游仍然持续有请求 (直接使用该上游)，上游会持续进行 ping 测试，间隔会缩短至约 5s。
      # 现有 ping 逻辑足以将服务不可用时间控制在几秒内。不建议更改该值。没必要。
      ping_interval: 120

load_balancers: # 负载均衡组
  - tag: "lb1" # (必需) 负载均衡组标签，需唯一。
    # 方案:
    # random: (默认) 随机选取后端。支持 weight 值。
    # qname_hash: 随机选取后端，但相同的 请求域名 将分配到相同的后端。支持 weight 值。
    # client_ip_hash: 随机选取后端，但相同的 客户端 IP 将分配到相同的后端。支持 weight 值。
    # fall_through: 顺序选取后端。支持 qps 值。一后端如果达到 qps 限制会尝试
    #   使用下一个后端。
    method: "random"
    # 后端组。如果后端设置了 health check。标记为 offline 的后端
    # 会被自动屏蔽 (请求不再分配给该后端)，online 后会恢复。
    backends:
      - tag: "google" # (必需) upstreams 的 tag。
        weight: 1 # 权重。默认 1。
        qps: 0    # QPS 限制。默认 0 无限制。

domain_sets: # 域名表。
  - tag: "" # (必需)标签，需唯一。
    files: [] # 文件(s)。

ip_sets: # IP 地址集合。
  - tag: "" # (必需)标签，需唯一。
    files: [] # CIDR 文件(s)。多个文件的条目取并集。

hosts: # 静态 hosts 表。
  - tag: "" # (必需)标签，需唯一。
    ttl: 300 # 返回的 A/AAAA/SOA TTL。默认 300。
    # mosdns 风格: 域名在前，后面可以跟多个 IPv4/IPv6。
    # 同一个域名出现在多条 entry 或多个文件中时，IP 会合并并去重。
    entries:
      - "github.com 140.82.112.4"
      - "dns.google 8.8.8.8 8.8.4.4 2001:4860:4860::8888"
    files: [] # hosts 文件(s)，格式同 entries，支持热重载。

rules: # 请求转发规则
  # 多个匹配之间的关系为 AND，有一个匹配不满足，规则就会被跳过。
  # 规则按顺序匹配。如果没有规则生效，会返回 REFUSE。
  # 匹配:
  - reverse: false # 规则取反
    domain: "" # 域名集的 tag。
    server: "" # server 的 tag。
    server_name: "" # TLS 连接的 SNI。
    path: "" # http 的 path，如果 "/" 结尾，则会匹配所有子 path。
    client_ip: # 客户端 ip。可以是单个 ip，cidr，ip 范围。
      - "192.168.1.100"
      - "192.168.1.0/24"
      - "192.168.1.100-192.168.1.255"
    # 处理:
    reject: 0 # > 0 会用这个 rcode 拒绝请求
    hosts: "" # hosts 的 tag。命中时直接返回 hosts 响应，未命中时继续后续规则。不能和 forward 配置在同一条 rule。
    forward: "google" # 出站的 tag。可以是 upstream，可以是 load_balancer。
    # 响应 IP 匹配。值为 ip_sets 的 tag。
    # 当上游应答的 A/AAAA 记录中任意一个 IP 落在该集合中时，视为"命中"。
    # 命中后会丢弃该响应，并继续尝试下一条规则。
    # 典型用途: 检测 DNS 污染。将已知的污染 IP 加入 ip_set，命中时自动回退。
    resp_ip: ""
    # 响应 IP 命中后的备用上游。值为 upstream 或 load_balancer 的 tag。
    # 必须与 resp_ip 配合使用。
    # 如果配置了该项，当 resp_ip 命中时，不再 continue 到下一条规则，
    # 而是直接使用该备用上游重新查询。备用上游的结果使用独立的缓存键。
    resp_ip_forward: ""

middleware: # 中间件设置
  - type: "" # 类型
  # ...      # 中间件参数

addons: {} # 保留。目前没用。
```

### ENV

* `MOSPROXY_JSONLOGGER=true` 可以让日志以纯 json 形式输出。性能更好并且便于日志处理。

### API

* `GET /metrics`: prometheus metrics
* `GET /ctl/reload`: 重载资源文件（ecs ip zone, ecs zone overwrite, domain set, ip set）。如果成功，返回 `200`。否则 `500`。只有全部文件成功重载才会应用更改，不会出现重载一半的情况。

## 文件格式

### 域名表文件

支持 3 种匹配规则。

格式: `[domain:|full:|regexp:]<规则> # 注释`

* `domain:` (默认) 域匹配。匹配这个域名和其子域。
* `full:` 全匹配。
* `regexp:` 正则匹配。

域匹配和全匹配非常高效。所有规则会优化成一个二进制索引，O(log(n)) 匹配复杂度。以载入 [Cloudflare radar 全球热门 100w 域名](https://radar.cloudflare.com/domains) 为例，文件大小 14M，载入用时约 1s，载入后二进制索引约 40M，匹配用时约 100ns。

正则匹配无法优化，是遍历匹配。会在 域匹配和全匹配 之后匹配。

### IP 集合文件

每行一个 CIDR，支持 IPv4 和 IPv6。`#` 后为注释，空行忽略。

格式: `<CIDR> # 注释`

示例:

```
# 已知的 DNS 污染 IP
1.2.3.4/32
203.0.113.0/24
2001:db8::/32 # IPv6 示例
```

多个文件的条目取并集。匹配时，只要响应中任意一个 A/AAAA 记录的 IP 落在集合中，即视为命中。

### ECS IP Zone 文件

格式: `<起始IP(包含)>,<结束IP(包含)>,<zone标识> # 注释`

IP 段不能有重叠。`<zone标识>` 不要过长，该字符串会成为缓存的 key 的一部分。

示例:

```
1.1.1.0,1.1.1.255,zone_name1 # comment
1.1.2.0,1.1.2.255,zone_name1
192.168.0.0,192.168.255.255,zone_name2
::1,::ffff,zone_name3
```

载入时整个文件会优化成一个二进制索引，O(log(n)) 匹配复杂度。100w 条规则索引约占 25M。

IP Zone 会匹配请求发往上游的 ECS 地址，并用 `<zone标识>` 标记请求。具有相同 `<zone标识>` 的请求将共享缓存。

### ECS Zone Overwrite 文件

格式: `<zone标识>,[<CIDR>|"-"] # 注释`

示例:

```
zone_name1,1.1.1.0/24 # comment
zone_name2,::1/128
zone_name3,-
,-
```

`<zone标识>` 不可重复。空 `<zone标识>` 可匹配没有 `<zone标识>` 的请求。`CIDR` 的后缀代表 ECS 的 mask。`-` 代表不转发 ECS。

具有 `<zone标识>` 请求转发至上游时将会附带指定的 ECS。

## 使用示例

### 基础: 单上游转发

```yaml
servers:
  - protocol: udp
    listen: "0.0.0.0:53"
  - protocol: tcp
    listen: "0.0.0.0:53"

upstreams:
  - tag: "google"
    addr: "https://dns.google/dns-query"
    dial_addr: "8.8.8.8"

rules:
  - forward: "google"
```

### 分流: 国内外域名使用不同上游

```yaml
upstreams:
  - tag: "alidns"
    addr: "https://dns.alidns.com/dns-query"
    dial_addr: "223.5.5.5"
  - tag: "google"
    addr: "https://dns.google/dns-query"
    dial_addr: "8.8.8.8"

domain_sets:
  - tag: "cn"
    files: ["cn_domain.txt"]

rules:
  - domain: "cn"
    forward: "alidns"
  - forward: "google"
```

### 防污染: 使用 resp_ip 检测并回退

```yaml
upstreams:
  - tag: "default"
    addr: "udp://223.5.5.5"
  - tag: "clean"
    addr: "https://dns.google/dns-query"
    dial_addr: "8.8.8.8"

ip_sets:
  - tag: "poisoned"
    files: ["poisoned_ips.txt"]

rules:
  - forward: "default"
    resp_ip: "poisoned"
    resp_ip_forward: "clean"
```

当 `default` 上游的响应 IP 匹配 `poisoned` 集合时，自动使用 `clean` 上游重新查询。

### Hosts: 静态域名解析

```yaml
hosts:
  - tag: "local"
    ttl: 300
    entries:
      - "github.com 140.82.112.4"
      - "dns.google 8.8.8.8 8.8.4.4 2001:4860:4860::8888"
    files: ["hosts.txt"]

upstreams:
  - tag: "default"
    addr: "8.8.8.8"

rules:
  - hosts: "local"
  - forward: "default"
```

`hosts` 命中 `A` 查询时返回全部 IPv4，命中 `AAAA` 查询时返回全部 IPv6。
如果域名命中但没有当前查询类型对应的 IP，例如只有 IPv4 但查询 `AAAA`，会返回 `NOERROR`、空 Answer 和一条 SOA，不再继续转发。
如果域名未命中，则继续尝试下一条规则。`hosts` 和 `forward` 不能配置在同一条 rule，需要转发兜底时请写成两条 rule。

### Bootstrap DNS: 无需系统 DNS 解析上游域名

```yaml
upstreams:
  # bootstrap 必须在前面定义，且 addr 为 IP 地址
  - tag: "bootstrap"
    addr: "tls://8.8.8.8"
  - tag: "cloudflare"
    addr: "https://cloudflare-dns.com/dns-query"
    bootstrap: "bootstrap"
```

`cloudflare` 上游的域名 `cloudflare-dns.com` 将通过 `bootstrap` 上游解析，无需依赖系统 DNS。

### 按上游关闭 ECS

```yaml
ecs:
  enabled: true

upstreams:
  - tag: "with_ecs"
    addr: "https://dns.alidns.com/dns-query"
    dial_addr: "223.5.5.5"
  - tag: "no_ecs"
    addr: "https://dns.google/dns-query"
    dial_addr: "8.8.8.8"
    no_ecs: true  # 该上游不发送 ECS
```

## 其他

* golang 支持的所有平台理论上都能编译并且能用，但本项目只针对 linux 开发/优化。

### Middleware 自定义请求处理逻辑

接口声明见 <https://github.com/IrineSistiana/mosproxy/blob/dev/app/router/router_middleware.go>

示例 <https://github.com/IrineSistiana/mosproxy/blob/dev/app/router/middleware/limit/limit.go>

## 版本状态

alpha
