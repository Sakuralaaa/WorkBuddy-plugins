# WorkBuddy-plugins

> 🚧 **开发中 / Work in progress** — 还不能加载到 CPA。

CLIProxyAPI（CPA）插件：把 WorkBuddy 国内版（copilot.tencent.com）和国际版（workbuddy.ai）接入 CPA，尽量还原 magpie 社区插件的行为，并参考 workbuddy2api-hub 补全自动签到与任务。插件 ID `workbuddy-magpie`，与插件商店的 `workbuddy` 不冲突。

## 进度

已完成（`internal/wb`，`go test` 通过）：
- 登录：浏览器登录（发起/轮询）、API Key、token JSON / 桌面端 `.info` 导入（5.6+ 加密文件会拒绝并说明）
- Token 刷新（单飞、失败沿用旧 token）、企业账号（X-Enterprise-Id / X-Tenant-Id、云托管企业域名）
- 请求改写：系统提示、工具与 schema 修正、思考回传、各类错误码重试（11153 / 11159 / 14018 / 11140 等）
- 流式处理与非流式聚合、模型列表（/v3/config）、额度读取、国内版每日签到
- 与 magpie 0.1.43 录制的 24 次请求逐字节一致（`internal/wb/sent_test.go`）

未完成：
- `internal/plugin`：quota、管理路由、调度器（目前 `go build ./...` 不通过）
- cgo 导出（`cmd/plugin`）、.so 构建、在 CPA 中加载实测
- 自动任务：成长任务、运营活动、小程序任务、加油站签到、猫猫旅行、国际版每日活跃（需真人完成的任务只提示不伪造）

## 风险

workbuddy2api-hub 称 WorkBuddy 自 2026-10-23 起收紧对非官方客户端的识别，使用有封号风险，请勿使用主账号。

## 致谢

见 [NOTICE](NOTICE)。MIT License。
