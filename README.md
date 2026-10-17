# ccguard

**为官方 Claude Code 提供独立登录配置、统一代理入口和连接诊断的 macOS 命令行工具。** 命令名为 `ccg`，使用 Go 标准库实现，无第三方 Go 依赖。

适合希望将 Claude 的账号配置与已有 CLI 环境分开、集中管理本地代理和专用 Chrome 的用户。ccg 启动已安装的官方客户端；实际路由和节点选择交给上游代理（例如 PopCat）。

- **独立配置**：CLI 使用单独的 Claude 配置目录，不导入默认目录中的认证令牌、API 地址或插件。
- **连接检查**：启动 CLI 前检查三个官方域名，运行期间定期检查；检查失败时阻止新的代理请求，上游恢复后自动放行。
- **专用浏览器**：用独立 Chrome 配置保存登录状态，并提供本地诊断页。
- **Desktop 启动入口（实验性）**：配置本地 Code 会话代理、创建 Dock 入口，支持按字段撤销。详见 [Desktop 使用说明](docs/DESKTOP.md)。

ccg 仅管理使用其代理配置的连接，不提供系统级网络隔离或账号可用性保证。

## 快速开始

准备好以下环境：

- macOS、Go 1.24 或更新版本。
- 已安装的官方 [Claude Code](https://code.claude.com/docs/en/setup)，且 `claude` 在 `PATH` 中。
- Google Chrome，默认位置为 `/Applications/Google Chrome.app`。
- 可用的 HTTP(S) 上游代理；不支持 SOCKS 地址或带密码的代理 URL。

在本仓库目录运行：

```sh
./install.sh
ccg doctor
ccg login
```

安装脚本将 `ccg` 和浏览器辅助链接 `ccg-browser` 安装到 `~/.local/bin`，并初始化配置。如果安装目录不在 `PATH` 中，需自行添加；脚本不改写 shell 配置。

初始化优先读取 macOS 已启用的 HTTPS 系统代理，否则使用 `http://127.0.0.1:7897`。上游地址不符时先修改，再诊断：

```sh
ccg config set upstream_proxy http://127.0.0.1:7897
ccg doctor
```

`ccg login` 调用官方 `claude auth login --claudeai`。在专用 Chrome 中自行完成登录和官方授权，然后检查状态，在项目目录启动：

```sh
ccg auth status
cd /path/to/your/project
ccg
```

ccg 不代管密码、不购买订阅，也不会自动发起模型对话。

## 日常使用

```sh
ccg                         # 保留当前工作目录与终端，启动官方 CLI
ccg -- --resume              # 继续会话
ccg -- --model sonnet        # 传入 Claude 参数
ccg browser                 # 在专用 Chrome 中打开 claude.ai
ccg browser doctor          # 打开本地浏览器诊断页
ccg doctor                  # 检查客户端、配置与上游连接
ccg status                  # 查看后台服务及最近的探测结果
ccg logs                    # 最近 40 行事件日志
ccg logs --service          # 服务启动/运行日志
ccg help
```

Claude 参数会原样传递，但 `--settings`、`--setting-sources` 和 `--bare` 与 ccg 的配置隔离冲突，会被拒绝。项目设置、`CLAUDE.md` 和权限确认仍由官方客户端处理；ccg 不自动开启跳过权限模式。

## 网络与后台服务

```text
官方 Claude Code / 专用 Chrome / 经 ccg 启动的 Desktop
                     ↓ HTTP(S) 代理 / CONNECT
             127.0.0.1:17897（ccg）
                     ↓ 固定上游，无直连回退
             127.0.0.1:7897（例如 PopCat）
                     ↓ 上游负责分流与节点选择
                   目标站点
```

CLI 和 Desktop 启动前要求网络检查通过；浏览器可在检查失败时打开，便于查看本地诊断页。服务按需启动，关闭客户端后仍继续运行，默认不注册登录启动项。

```sh
ccg service start
ccg service stop
ccg service restart
ccg service install         # macOS 用户登录时启动
ccg service uninstall       # 移除登录启动项并停止其托管的服务
```

服务默认每 60 秒并行访问 `api.anthropic.com`、`claude.ai`、`platform.claude.com` 的 `/cdn-cgi/trace`。探测失败时阻止新请求和新隧道；单次检查失败不会主动关闭已有隧道。停止服务会关闭已有隧道，重要会话期间应避免重启。已由 Claude 执行的本地工具不会被撤销。

HTTP 200 且包含有效 trace 字段时显示出口 IP/地区；其他 HTTP 状态仅说明 TLS/HTTP 可达，不代表账号或模型可用。探测不发送账号 Cookie、令牌、代码或提示词。地区变化写入事件日志，普通 IP 变化不视为账号风险。

如需限定探测出口地区：

```sh
ccg config set expected_country SG
ccg service restart
ccg config set expected_country none  # 取消要求；随后重启服务生效
```

设置后，三个域名的探测地区必须全部匹配才能放行。这只是该次请求的连接一致性检查，不改变实际所在地、使用资格或服务端判断。

## 配置与数据目录

```sh
ccg config path
ccg config show
ccg config set disable_nonessential_traffic false
ccg config set timezone Asia/Singapore
ccg config set timezone none
```

`config set` 支持 `upstream_proxy`、`expected_country`、`disable_nonessential_traffic`、`timezone`、`desktop_app`。监听地址、客户端路径、数据目录和探测时间需直接编辑 `config.json`；未知字段、非法端口和多余的 JSON 内容会被拒绝。

| 配置项 | 默认行为 / 约束 |
| --- | --- |
| `listen` | `127.0.0.1:17897`；仅允许回环 IP 和 1–65535 的固定端口 |
| `upstream_proxy` | 初始化时检测系统 HTTPS 代理，否则 `http://127.0.0.1:7897` |
| `expected_country` | 空，不限定地区；可设两个大写字母的地区代码 |
| `check_interval_seconds` | 60；允许 10–3600 秒 |
| `connect_timeout_seconds` | 10；允许 1–120 秒 |
| `disable_nonessential_traffic` | `true`；影响自动更新及部分依赖功能标志的功能，可手动 `claude update` |
| `timezone` | 不覆盖；可选 IANA 时区，仅通过 `TZ` 传给本地进程 |
| `desktop_app` | 自动查找 `/Applications/Claude.app`、`~/Applications/Claude.app` |

客户端路径、时区和非必要流量选项只影响新启动的客户端；Desktop 设置需重新运行 `ccg desktop setup` 并完全退出后重开应用。上游、监听地址、地区要求、检查间隔或超时改变后，需要重启代理服务。

| 用途 | 默认位置 |
| --- | --- |
| 配置 | `~/.config/ccg/config.json` |
| CLI 的独立 Claude 配置与会话 | `~/.config/ccg/claude`；凭据实际存储由官方客户端管理 |
| 专用 Chrome 配置及 Cookie | `~/.local/share/ccg/chrome` |
| 控制套接字、日志、Desktop 恢复记录与备份 | `~/.local/state/ccg` |

CLI 通过官方 `CLAUDE_CONFIG_DIR` 隔离用户配置，默认 `~/.claude/settings.json` 保持原样。**Desktop 模式会修改默认设置文件**，具体范围和撤销方式见 [Desktop 使用说明](docs/DESKTOP.md)。

`CCG_CONFIG=/绝对路径/config.json` 可指定独立实例；状态、Chrome 和 Claude 数据默认保存在该文件所在目录下。不同实例还需使用不同的监听端口；登录启动项和 Desktop 启动入口为当前用户共享。**测试 Desktop 还需隔离 `HOME`**，因为它始终配置当前 `HOME` 下的 `~/.claude/settings.json`。

新建的配置、套接字和日志权限为 0600，数据目录为 0700；ccg 不自动收紧已存在目录的权限。停止服务或卸载不会清空 Cookie、登录状态或备份。

## 专用 Chrome 与 PopCat

Chrome 使用独立 `user-data-dir` 和固定本地代理，不复用普通 Chrome 的 Cookie、扩展和同步配置，不安装自动化扩展，也不开启远程调试端口。修改监听地址或浏览器配置后，需手动关闭全部 ccg 专用 Chrome 窗口再重开；已运行的 Chrome 实例不会重新应用启动参数。

诊断页显示浏览器实际读取的时区、语言、User-Agent 和位置权限状态，不请求精确位置。点击检查后，浏览器直接请求三个 trace 地址且不携带 Cookie；跨域限制会明确显示为无法读取。设置 `TZ` 不保证 Chrome、Electron 或所有本地工具采用同一时区，也不修改 macOS 系统时区。

登录通过 `BROWSER=ccg-browser` 请求专用 Chrome；客户端版本若不采用该变量，可复制官方登录 URL 后手动打开：`ccg browser 'URL'`。请勿分享带 OAuth 参数的 URL。

```sh
ccg popcat doctor            # 只读检查保存的配置与本地端口
ccg popcat export [FILE]     # 导出可选分流模板，不自动导入
```

PopCat 不是必选依赖。ccg 不读取订阅令牌、不修改全局选项、不自动更换节点。可选模板来自唯一的 [popcat/ccguard.module](popcat/ccguard.module)，通过 Go embed 内置；需在 PopCat 的模块配置中手动导入并选择线路，不包含自动测速、fallback、HTTPS 解密或重写。

## 隐私与适用边界

- 不解密 TLS、不安装根证书、不关闭证书验证，不修改设备指纹或系统提示词。
- 事件日志只保存时间、目标主机与结果，不保存正文、查询参数、令牌或密码；超过 1 MiB 时轮转一次。
- 本地控制接口使用 0600 的 Unix 套接字，代理仅监听回环地址。
- 检查已知设置文件中的认证冲突和文件型托管配置；不完整枚举 MDM、服务端或宿主下发的组织策略，无法覆盖它们。
- MCP、项目 hooks、外部命令、更新器及其他独立联网组件可能不采用此代理。Desktop 的用户设置还可能被项目或托管设置覆盖。

代理就绪不代表账号可用、全部流量受控或地区使用资格符合要求。服务端规则仍由服务商决定，Desktop 真实网络覆盖及客户端时区行为仍需实际验收。

## 开发与卸载

```sh
go test -race ./...
go vet ./...
go build -trimpath -o bin/ccg ./cmd/ccg
~/.local/bin/ccg uninstall
```

测试使用本地模拟上游，覆盖 CONNECT 缓冲字节、HTTP 流式响应及截断、TLS 校验、配置与环境隔离、Desktop 按字段恢复和服务启停，不调用付费模型接口。构建后若需使用登录浏览器辅助入口，请通过 `./install.sh` 安装。

卸载会先撤销 ccg 管理的 Desktop 字段和入口，再移除登录启动项、停止服务并删除安装命令，保留配置、登录和备份。遇到外部修改时停止撤销，保留命令供排查；自动卸载仅支持默认的 `~/.local/bin` 安装位置。

代码按职责集中在 `internal/guard`：`cli.go` 负责命令入口，`config.go` 负责配置，`claude.go`/`browser.go`/`desktop.go` 负责客户端，`proxy.go`/`service.go` 负责转发与常驻服务。审查结论及后续边界见 [设计与实现审查](docs/DESIGN-REVIEW.md)。

官方参考：[配置与优先级](https://code.claude.com/docs/en/settings)、[代理配置](https://code.claude.com/docs/en/network-config)、[认证](https://code.claude.com/docs/en/authentication)、[Desktop](https://code.claude.com/docs/en/desktop)、[支持地区](https://www.anthropic.com/legal/supported-regions)。
