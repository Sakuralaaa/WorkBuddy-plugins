# WorkBuddy-plugins

CLIProxyAPI（CPA）插件：把 WorkBuddy 国内版（copilot.tencent.com）和国际版 WorkBuddy AI（workbuddy.ai）接入 CPA，并在 CPA 管理面板里提供一个完整的 **WorkBuddy** 页面：登录两个版本的账号、管理账号、看积分、选模型、改设置，以及自动签到 / 连登 / 猫猫旅行 / 成长任务的任务中心。插件 ID `workbuddy-magpie`，与插件商店的 `workbuddy` 不冲突，两个提供方：

| 提供方 | 版本 |
|---|---|
| `workbuddy-magpie` | 国内版 WorkBuddy |
| `workbuddy-magpie-ai` | 国际版 WorkBuddy AI |

> ⚠️ **风险提示**：据 workbuddy2api-hub，WorkBuddy 自 **2026-10-23** 起收紧了对非官方客户端的识别，使用本插件（尤其是自动任务）有被限制或封号的风险，**请勿使用主账号**。需要真实捐款（`Expert_Philanthropy`）、关注微信公众号、手机 App 内真实对话等只能由真人完成的任务，插件只提示、**不伪造**。

## 安装

### 从插件市场安装

在 CPA 的 `config.yaml` 加上本仓库的商店源（CPA 读的是 registry.json 地址，不是仓库主页）：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/Sakuralaaa/WorkBuddy-plugins/main/registry.json"
```

重启后管理面板的插件市场里会出现 “WorkBuddy（国内版 + 国际版）”，点安装即可。发布由 `v*` 标签触发 GitHub Actions 构建 linux amd64/arm64。

### 自己编译

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o workbuddy-magpie.so ./cmd/plugin
```

把 `.so` 放进 CPA 的 `plugins.dir`，并在 `config.yaml` 里设 `plugins.enabled: true`、`plugins.configs.workbuddy-magpie.enabled: true`。

## WorkBuddy 页面

插件加载后，CPA 管理中心左侧菜单会多出 **WorkBuddy**（插件资源 `/v0/resource/plugins/workbuddy-magpie/index.html`，在管理中心里以内嵌页打开）。

**鉴权**：插件没有任何自己的密钥。页面里每个请求都只用 **CPA 的管理密钥**（`remote-management.secret-key`）鉴权：页面与管理中心同源，会直接读取管理中心保存在浏览器里的登录信息（`cli-proxy-auth`，按管理中心同样的方式解混淆）。只有在拿不到时（登录管理中心时没勾选「记住密码」，或管理中心与 CPA 不同源）才会提示输入一次管理密钥，页面会说明原因。资源路由本身只返回静态页面，不含任何数据；所有数据接口都在需要管理密钥的 `/v0/management/` 下。

页面能用 CPA 自带接口完成的都直接调 CPA：

| 功能 | 调用 |
|---|---|
| 浏览器 OAuth 登录（国内版 / 国际版） | `GET /v0/management/workbuddy-magpie-auth-url`、`/workbuddy-magpie-ai-auth-url` + `GET /v0/management/get-auth-status?state=`，CPA 自己写凭据文件 |
| 账号列表、启用/停用、删除、刷新令牌 | `GET /auth-files`、`PATCH /auth-files/status`、`DELETE /auth-files`、`POST /auth-files/refresh` |
| API Key 登录、token JSON / 桌面端 `.info` 导入 | `POST /auth-files?name=…`（API Key 先由插件向 WorkBuddy 校验） |
| 保存设置 | `PATCH /v0/management/plugins/workbuddy-magpie/config` |
| 测试模型 | `GET /v0/management/api-keys` 取 CPA 的客户端 api-keys，再调 CPA 自己的 `/v1/chat/completions` |

插件只补 CPA 没有的接口（都要管理密钥）：

| 路由 | 用途 |
|---|---|
| `GET workbuddy-magpie/accounts` | 每个 WorkBuddy 凭据的 UID、企业、套餐、令牌到期、适用任务、上次任务结果 |
| `GET workbuddy-magpie/credits[?account=]` | 积分窗口（套餐/额外/合计/预付）、积分包明细与到期、全部账号汇总 |
| `GET workbuddy-magpie/models?build=cn\|ai` | 实时模型列表（价格、免费、思考档位、图片） |
| `GET workbuddy-magpie/tasks?account=` | 任务中心：成长任务（含小程序任务）进度/奖励/完成方式、连登/补签卡/抽奖/能量/猫猫状态、运营活动进度 |
| `POST workbuddy-magpie/run` | `{account, job, task}` 立即运行；`account:"*"` 全部账号；空 body = 所有已开启任务 |
| `GET workbuddy-magpie/status` | 调度器状态、正在运行、各账号上次结果、运行记录 |
| `POST workbuddy-magpie/keys/check` | 保存前校验 API Key |
| `POST workbuddy-magpie/history/clear` | 清空运行记录 |

页面分区：

1. **账号**：两个版本的全部账号，版本徽标、名称/UID、企业、套餐、令牌到期、状态，刷新令牌 / 积分 / 任务 / 启用停用 / 删除。
2. **添加账号**：先选国内版或国际版，再选浏览器登录（解决了 CPA 原 OAuth 页不显示国际版登录的问题）、API Key 或 token JSON / `.info` 导入。
3. **积分**：每个账号的额度窗口和积分包，顶部汇总。
4. **模型**：实时模型表；免费模型开关（`free_only` / `free_only_cn` / `free_only_ai`）、`hide_dsh`、App/CLI 版本、`sdk_os`/`sdk_arch`；测试模型。
5. **任务中心**：每个账号的任务状态、进度、奖励，「一键完成」、单个任务执行/领取、全部账号一键完成。
6. **调度设置**：调度总开关、自动领奖、每个任务的开关与北京时间。
7. **状态与日志**：调度器状态、正在运行的任务、运行记录（含每次的详细日志、积分）、「立即运行」。

模型调用照常走 CPA：客户端用 CPA 配置里的 `api-keys` 调 `/v1/chat/completions` 等接口即可，插件不引入任何调用密钥。

## 任务

| 任务（配置键） | 适用 | 做什么 |
|---|---|---|
| 每日签到（`checkin`） | 国内版 | Buddy 加油站签到，每北京日一次 |
| 对话活跃上报（`tasks.activity_report`） | 国内 + 国际 | 每天一条 `chat_request_send`，点亮成长中心连登 |
| 连登管家（`tasks.rewards`） | 国内版 | 昨日漏签且有补签卡时补签 → 新手礼包 → 先查补偿状态再领补偿 → 兑换已解锁的 7/14/28 天档位 → 抽完抽奖次数 |
| 猫猫旅行（`tasks.cat_travel`） | 国内版 | 到站领奖 → 空闲时派出（每日一次；没有 Buddy 先领养）→ 用多余能量开 Buddy 盲盒 |
| 成长任务（`tasks.growth`） | 国内版个人 | 接取 → 完成 → 等待异步计分 → 领奖。可自动：`chat_5`、`first_buddy`、`Model_chat_GLM5.2`、`RichMeow_Chat`、`Buddy_App`、`Buddy_App_QQ`、`automation_1`、`Library_read`、`template_5`、`playbook_prompt`、`create_canvas`、`expert_5`、`Expert_team_use_3`、`Hp_Appearance`、`skill_1`、`Expert_lighthouse`、`black_cat`（夜间）共 17 个，另有 `skill_share`（创建真实分享渠道）和链接里指定专家的活动任务。只提示不做：`Expert_Philanthropy`（需真实捐款）、`wb_wechat_oa_subscribe_task`（需关注公众号），以及没有接口可完成的其他任务 |
| 运营活动（`tasks.campaigns`） | 国内版 | 星巴克协作、记者节福利等活动任务，领奖后回读确认；「解锁 App 强大能力」需手机 App 真实对话，只提示 |
| 小程序任务链（`tasks.miniprogram`） | 国内版 | `Sequential_Tasks_1~7`、`school_season`；小程序对话事件按约 45 秒的真人节奏上报（连发会被判无效回滚），较慢 |
| 夜猫子（`tasks.night`） | 国内版 | 23:00–08:00 内 glm-5.2 真实对话一次并上报，每晚计 1 次，累计 3 晚领奖 |
| 国际版每日活跃（`tasks.intl_daily`，`tasks.intl_daily_web`） | 国际版 | 桌面通道一次短对话；开启 `intl_daily_web` 后再建一条网页云端会话并通过 ACP 跑完（计入每日活跃积分的那一条），结束后停止会话释放名额 |
| 补领清扫（`tasks.sweep`） | 国内版个人 | 每 `schedule.sweep_minutes`（默认 15 分钟）接取并领取所有已达标的任务：只做接取 + 领奖，不跑行为链、不看任务白名单。补上成长任务错过的部分——在官方客户端手动做完的、在成长任务时段之后达标的、新下发的任务 |
| 一键完成任务 | 国内版个人 | 不是配置开关，是管理页 `POST workbuddy-magpie/run` 传 `{"job": "tasks"}`：一次跑完补领清扫 → 成长任务 → 小程序任务链 → 运营活动 |

`tasks.claim`（默认 `true`）关掉后，任务只推进进度、不领奖（礼包、补偿、档位兑换、抽奖、旅行领奖也一并不做）。

部分任务需要少量真实对话（glm-5.2 / fast-model，各几条短消息），走插件自己的请求路径（同样的签名和 CPA 的代理）；开了「只用免费模型」时，非免费模型的对话会被拒绝，相应任务会报失败而不是伪造。所有上游请求都经 CPA 的 `host.http.*` 发出，使用 CPA 的代理设置。同一账号的任务串行执行，上游写操作之间至少间隔 1 秒；不同账号并行跑（`tasks_concurrency`，默认 2，最多 4）。

## 指纹与粘性会话

请求指纹按桌面客户端的完整头族发出：`User-Agent: WorkBuddy/<版本> WorkBuddy/<版本> CLI/<版本>`、`X-IDE-Name/Version/Type`、`X-Agent-Intent/Purpose/Type/Runtime`、`X-Stainless-*`，对话另带 `X-Conversation-ID / X-Conversation-Request-ID / X-Root-Request-ID / X-Conversation-Message-ID`（同一会话同一轮次用同一组 id，换账号时按新账号重算，提示缓存不跨账号共享）以及 `prompt_cache_key`。`X-Device-Token` 三个来源按优先级取用：账号凭据里的 `device_token` → 配置 `device_token` → 配置 `device_token_file` 指定的文件（桌面端生成的 token，5 分钟缓存，超过 1KB 忽略）；三个都没有就不发这个头。

粘性会话：会话 id 从请求头/元数据里取，取不到时再从请求体 `conversation_id` 或 `metadata.session_id` 里取。会话与首次服务的账号绑定记录在状态文件里（只存会话 id 的哈希，不存原文），24 小时滚动过期。注意：**选哪个账号是 CPA 路由决定的，插件改不了**——会话换账号时插件记录一次跳号并改绑到新账号（之后在新账号上的轮次不再重复计数）（状态页 `status` 的 `sticky` 字段可见）、记一条警告日志，然后继续用新账号的 id 发请求。想让一个会话真正钉在一个账号上，要在 CPA 侧用单账号的路由组/渠道。

签到失败会按 15 分钟、30 分钟、1 小时、2 小时、4 小时（封顶）翻倍退避重试，直到当天落定（已领/已签/无资格/活动未开都算落定，当天不再跑）；以前失败一次就把时段标记为已执行，整天不再重试。

补偿状态：`GET workbuddy-magpie/compensation`（可带 `?account=`）列出各账号的补偿包、截止时间，48 小时内到期的标 `urgent`；查询失败单独成行报错，不会跟“没有补偿”混在一起。

免费额度窗口：`deepseek-v4.1-flash` 有自己独立的 token 额度（国内版约 2 亿、国际版 1 亿），跟积分是两本账，积分一分没动也可能被它拒掉。插件每次经 CPA 转发这个模型时记一行（时间、账号、token 数、429 的报错原文），按 magpie 的链式算法算当前窗口：窗口从上一个窗口结束后第一个被接纳的请求开始，24 小时；429 里厂商给的重置时间是窗口的硬边，正在生效的拒单直接按满额显示、标“已触发限流”。结果出现在积分页的额度卡片里（标“估算”）和 CPA 的额度视图里。行存在凭据目录下的 `.workbuddy-magpie-freewin`，只记数字和拒单原文，不记提示词和回答，30 天以上的行自动丢弃。注意两点：这个数是估算——账号在别处（官方客户端、别的网关）用的量插件看不见，真实值只看厂商的 429；任务里自己的短对话（`chatFor`）量极小，没记进来。

## 配置

```yaml
plugins:
  configs:
    workbuddy-magpie:
      enabled: true
      free_only: false          # 两个版本都只用免费模型
      # free_only_cn: true      # 单独设置国内版
      # free_only_ai: true      # 单独设置国际版
      hide_dsh: true
      # cn_app_version / cn_cli_version / ai_app_version / ai_cli_version
      # sdk_os / sdk_arch
      # device_token: ""        # 全局 X-Device-Token；账号凭据里的 device_token 优先
      # device_token_file: ""   # 桌面端 token 文件路径（兜底来源）
      # tasks_concurrency: 2    # 调度器并行跑几个账号（1–4）
      scheduler: true           # false 关闭后台调度（手动运行仍可用）
      checkin: false
      tasks:
        activity_report: false
        rewards: false
        cat_travel: false
        growth: false
        campaigns: false
        miniprogram: false
        night: false
        intl_daily: false
        intl_daily_web: false
        sweep: false
        claim: true
      schedule:                 # 北京时间
        checkin_at: "09:00"     # 空 = 0 点后第一次检查
        activity_hours: [9, 21]
        rewards_hours: [9, 21]
        travel_hours: [9, 21]
        growth_hours: [10]      # 成长任务、运营活动、小程序任务共用
        night_hours: [1]
        sweep_minutes: 15       # 补领清扫间隔（分钟）；0 = 只手动跑
        intl_daily_times: ["10:30", "20:30"]
```

**设置如何保存、何时生效**：页面保存时调用 CPA 自带的 `PATCH /v0/management/plugins/workbuddy-magpie/config`，写进 `config.yaml` 的 `plugins.configs.workbuddy-magpie`（浅合并，`null` 删除该键），CPA 随即热重载并通过 `plugin.reconfigure` 把新配置交给插件，**不需要重启**。`config.yaml` 是唯一的设置来源，手工编辑同样生效；插件不另存设置。

**调度规则**：调度器每分钟检查一次。每个任务在设定时刻到达后、该时段还没执行过时执行一次（CPA 晚于设定时刻启动也会补跑当天最近的那一档）；签到、活跃上报、国际版每日活跃当天成功一次后不再重复。停用的账号不参与。

**状态文件**：各账号的上次结果、已执行时段和最近 120 条运行记录保存在 CPA 凭据目录下的 `.workbuddy-magpie-state`（不以 `.json` 结尾，CPA 不会把它当凭据读取）；内存里另保留最近 300 条。

## 开发

```bash
go vet ./...
go test ./...   # 含与 magpie 0.1.43 录制的 24 次请求逐字节一致的测试（internal/wb/sent_test.go）
```

- `internal/wb`：magpie 移植（登录、刷新、请求改写、流式、模型、额度、签到）
- `internal/jobs`：任务实现（移植自 workbuddy2api-panel 与 workbuddy2api-hub），只依赖 `wb.Doer`，测试用假服务器覆盖
- `internal/plugin`：CPA 插件层、调度器、管理接口、内嵌页面 `internal/plugin/web/index.html`

## 致谢

见 [NOTICE](NOTICE)。MIT License。
