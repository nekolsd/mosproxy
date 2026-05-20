# mosproxy

Fork 自 [IrineSistiana/mosproxy](https://github.com/IrineSistiana/mosproxy)，一个高性能 DNS 转发/代理软件。

## 相对上游的改动

- **响应 IP 匹配 (`resp_ip`)**: 支持根据上游响应中的 A/AAAA 记录 IP 匹配规则，可用于检测 DNS 污染并自动跳过该规则。
- **响应 IP 备用上游 (`resp_ip_forward`)**: 配合 `resp_ip` 使用，命中时自动切换到备用上游重新查询，单条规则内完成"主上游 → 检测污染 → 备用上游"的回退逻辑。
- **IP 集合 (`ip_sets`)**: 新增顶层 IP 地址集合定义，类似 `domain_sets`，支持从 CIDR 文件加载，支持热重载。
- **Hosts (`hosts`)**: 支持 mosdns 风格 hosts 表，一个域名可配置多个 IPv4/IPv6 地址，支持文件热重载。
- **Bootstrap DNS (`bootstrap`)**: 上游可指定另一个上游作为引导解析器，用于解析上游域名地址，无需依赖系统 DNS。
- **按上游关闭 ECS (`no_ecs`)**: 可为单个上游禁止发送 ECS，即使全局 ECS 已启用。
- **日志文件输出 (`log.file`)**: 支持将日志输出到指定文件，追加模式，无颜色人类可读格式，适配 logrotate。

## 文档

[Wiki](docs/wiki.md)

## License

[GNU General Public License v3.0](LICENSE)
