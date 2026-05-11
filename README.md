# Codex Gate

**Codex Gate** 是一个本地协议网关，让 Claude Code、CCR 等 Anthropic-compatible 客户端可以连接到 Codex / OpenAI-compatible 后端，同时尽量保留客户端原生的工具循环、流式输出和多轮工作方式。

English version: [README.en.md](README.en.md)

Codex Gate 是独立开源项目，不隶属于 OpenAI、Anthropic 或 Claude Code 官方项目。

## 项目定位

Codex Gate 关注一件事：在本机或受控环境里，把客户端请求转换为 Codex-compatible upstream 请求，并把结果转换回 Anthropic Messages / OpenAI-compatible 响应。

当前默认路径是：

```text
Claude Code / CCR
        |
        v
Codex Gate local gateway
        |
        v
Codex-compatible backend
```

当前 release 默认后端是 `codex-web`。它使用 ChatGPT Codex backend 路径，适合本地实验和受控自用场景；相比正式 OpenAI Responses API，它存在稳定性、账号策略和服务条款风险。未来是否把标准 OpenAI Responses API 设为默认后端，会在后续版本中单独决策。

## 核心特性

- Anthropic Messages API 兼容入口：面向 Claude Code / CCR 等客户端。
- OpenAI Chat Completions 兼容入口：用于通用代理或调试。
- SSE 流式转换：支持文本、工具调用事件和 keepalive。
- Codex Web token 注入：支持环境变量、受信 token helper 和交互式输入。
- 本地安全边界：不落盘 token，脚本只打印 token 状态，不打印 token 内容。
- Runtime / Dev 双包：用户运行包和开发审计包分离。
- Go runtime：长期网关运行时使用 Go；Python 只用于验证、打包、fixtures 和报告脚本。

## 不做什么

- Active backend: Codex only.
- 不引入 OpenClaw、Hermes 或外部编排层。
- Disabled: OpenClaw, Hermes, Claude backend fallback.
- 不把 Claude Code 当作后端执行 worker；Claude Code 只是客户端。
- 不执行生产写入、自动部署、自动回滚或云资源修改。
- 不读取、打印或复制 secrets。
- 不通过关闭测试、lint、benchmark 或 CI policy 来制造“通过”。

## 快速开始

### 1. 准备 token

任选一种方式提供 Codex Web access token：

```sh
export CODEX_ACCESS_TOKEN="..."
```

或使用 token helper：

```sh
sh scripts/install_codex_token_command.sh
```

脚本不会把 token 写入仓库。真实使用时建议用本地受信 helper，而不是把 token 放进 shell history。

### 2. 启动本地 gateway

```sh
sh scripts/start_local_codex_gateway.sh --port 18080
```

启动成功后脚本会打印：

- backend
- base URL
- health URL
- token 来源状态
- 下一步检查命令
- Claude Code 直连配置命令

### 3. 检查 gateway

```sh
sh scripts/check_local_codex_gateway.sh --base-url http://127.0.0.1:18080 --model gpt-5.5
```

### 4. 写入 Claude Code 直连配置

```sh
sh scripts/write_claude_code_direct_settings.sh --gateway-base-url http://127.0.0.1:18080
```

然后在目标项目目录运行：

```sh
claude
```

如果当前 shell 同时存在 `ANTHROPIC_API_KEY` 和 `ANTHROPIC_AUTH_TOKEN`，先清理冲突变量，或使用兼容 wrapper：

```sh
sh scripts/run_claude_code_direct.sh --gateway-base-url http://127.0.0.1:18080
```

## Release 包

Codex Gate 提供两类 release archive：

- `codex-gate-runtime.zip`：面向用户，只包含 gateway binary、启动脚本、检查脚本和必要文档。
- `codex-gate-dev.zip`：面向审计和继续开发，包含源码、fixtures、测试和验证脚本。

公开源码和 release 包不会包含 `.agents/`、`.codex/`、内部 docs、dogfood/review reports、PR 模板、缓存或 secrets-like 文件。Runtime 包还会排除源码目录、fixtures、benchmark 输出和 CI 文件。

## 本地开发验证

常用验证命令：

```sh
python scripts/validate.py
python scripts/scan_secrets.py
python scripts/test_protocol_fixtures.py
python scripts/test_phase9_review_fixes.py
```

如果本机有 Go：

```sh
go test ./...
```

打包并检查 release：

```sh
python scripts/package_release.py --kind runtime --output dist/release/codex-gate-runtime.zip
python scripts/inspect_release_archive.py --kind runtime --archive dist/release/codex-gate-runtime.zip
python scripts/package_release.py --kind dev --output dist/release/codex-gate-dev.zip
python scripts/inspect_release_archive.py --kind dev --archive dist/release/codex-gate-dev.zip
```

## 文档

- 使用手册：[USER_MANUAL.md](USER_MANUAL.md)
- 英文 README：[README.en.md](README.en.md)

公开仓库刻意不包含内部阶段计划、dogfood 报告、agent prompt、个人 workspace 文档或本地调研笔记。

## 安全说明

Codex Gate 是本地网关，不是托管服务。请只在你信任的机器和网络环境中运行它。不要把个人 token、ChatGPT/Codex 登录态、`.env`、SSH key 或云凭据提交到仓库或 release 包。

默认 `codex-web` 后端属于实验性路径。用于团队或生产前，请先审查上游账号策略、服务条款、token 存储方式、网络边界和日志脱敏策略。

## License

Codex Gate 使用 [Apache License 2.0](LICENSE) 开源。请同时保留 [NOTICE](NOTICE) 中的项目声明。
