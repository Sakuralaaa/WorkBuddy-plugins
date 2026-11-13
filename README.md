# WorkBuddy-plugins

> 🚧 **开发中 / Work in progress** — 已能编译并加载到 CPA（v8 插件 ABI），尚未用真实账号实测。

CLIProxyAPI（CPA）插件：把 WorkBuddy 国内版（copilot.tencent.com）和国际版（workbuddy.ai）接入 CPA，尽量还原 magpie 社区插件的行为，并参考 workbuddy2api-hub 补全自动签到与任务。插件 ID `workbuddy-magpie`，与插件商店的 `workbuddy` 不冲突。

## 进度

已完成（`internal/wb`，`go test` 通过）：
- 登录：浏览器登录（发起/轮询）、API Key、token JSON / 桌面端 `.info` 导入（5.6+ 加密文件会拒绝并说明）
- Token 刷新（单飞、失败沿用旧 token）、企业账号（X-Enterprise-Id / X-Tenant-Id、云托管企业域名）
- 请求改写：系统提示、工具与 schema 修正、思考回传、各类错误码重试（11153 / 11159 / 14018 / 11140 等）
- 流式处理与非流式聚合、模型列表（/v3/config）、额度读取、国内版每日签到
- 与 magpie 0.1.43 录制的 24 次请求逐字节一致（`internal/wb/sent_test.go`）

- CPA 插件层：两个提供方 `workbuddy-magpie`（国内版）/ `workbuddy-magpie-ai`（国际版），登录、刷新、模型、执行器、额度
- 调度器：国内版每日签到（配置 `checkin: true`，每北京日一次）；管理接口 `GET workbuddy-magpie/status`、`POST workbuddy-magpie/run`
- 已在本地 CPA 中加载，日志显示 plugin registered，管理接口列出两个提供方

## 编译

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o workbuddy-magpie-v0.1.0.so ./cmd/plugin
```

把 `.so` 放进 CPA 的 `plugins.dir`，并在 `config.yaml` 里设 `plugins.enabled: true`、`plugins.configs.workbuddy-magpie.enabled: true`。

未完成：
- 真实账号端到端测试
- 自动任务：成长任务、运营活动、小程序任务、加油站签到、猫猫旅行、国际版每日活跃（需真人完成的任务只提示不伪造）

## 风险

workbuddy2api-hub 称 WorkBuddy 自 2026-10-23 起收紧对非官方客户端的识别，使用有封号风险，请勿使用主账号。

## 致谢

见 [NOTICE](NOTICE)。MIT License。

## 从插件市场安装

在 CPA 的 `config.yaml` 加上本仓库的商店源（CPA 读的是 registry.json 地址，不是仓库主页）：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/Sakuralaaa/WorkBuddy-plugins/main/registry.json"
```

重启后管理面板的插件市场里会出现 “WorkBuddy（国内版 + 国际版）”，点安装即可。发布由 `v*` 标签触发 GitHub Actions 构建 linux amd64/arm64。
