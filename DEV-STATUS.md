# sub2api 日报/周报/月报功能 — 项目状态（供新 session 接续）

> 本文档是完整项目上下文。最后更新：2026-11-25（**报告生成任务队列 v4.6（已发布 stable）**：批量生成改为「先上账、再干活」——每个用户每周期先写一条 pending 任务，worker 带租约并发 4 路领取；进程重启/预算耗尽/某个用户卡住都会自动续跑，管理端可看「今天还有几个人没生成」。提示词审计扫描器已于同日恢复：根因 MiniMax `MiniMax-M2` 下线 + base_url 缺 `/v1` + 编码套餐 key RPM 低；已切 DeepSeek `deepseek-chat`、`input_limit=16000`、`worker_count=2`，新配置任务 0 失败。见 §8「v4.6」与「生产修复：提示词审计扫描器恢复」）。

## 1. 功能与当前状态总览

在自部署 sub2api（Wei-Shaw/sub2api，AI API 网关）上新增**日报/周报/月报**能力：

1. ✅ **Prompt 存储**（核心，已完成）：异步审计落库 `prompt_audit_events`，**扫描失败也必存**（降级落库）
2. ✅ **fork 改造**：Web 界面（admin 看所有人 / user 看自己）、定时生成、手动生成、LLM 设置页
3. ✅ **已上线生产**（33333，`sub2api:stable`）
4. ✅ **月报类型**（2026-09-16）：迁移 233 放宽 CHECK；月报固定覆盖 ref 的**上一个自然月**（每月 1 日 20:20 生成上月，手动生成语义一致）——v3 将反转为 ref 所在自然月，见 §8；聚合优先级：当月周报 → 当月日报 → prompt 片段
5. ✅ **LLM 总结 prompt 重写 ×2**（2026-09-16）：最终格式对齐团队模板——**标题由后端拼**（`reportTitle()`，姓名取 users.username，如 `# 工作日报（2026-09-15）- 谢翔宇`），LLM 只输出两个小节（`## 一、今日/本周/本月核心工作` + `## 二、明日/下周/下月工作计划`，平铺编号条目，无分类/优先级标注）；素材规则保留噪音过滤/合并同类/量化/脱敏；`ReportRepository.GetUsername()` 新增
6. ✅ **报告素材双通道**（2026-09-16 晚）：①`FetchUserTurns` 逐请求头部提取用户真实输入（剥 <system-reminder> 前缀+噪音过滤+去重）——普通聊天/Claude Code 客户端有效；②`FetchPromptSnapshots`+对话区窗口采样（锚点 `</available_skills>` 后，每 session 最全快照×均分窗口×700 字）——opencode 等智能体客户端用户输入埋在历史深处、且 reminder 字符串会出现在系统提示词讲解和文件内容里导致正则剥离不可靠，只能靠窗口采样。system prompt 含 ❌/✅ 反例（禁止'用了什么工具/模式/多少请求'类条目）。验证：谢翔宇 9/16 预览输出已为真实工作条目。**已知限制**：单 session 超 64k 时审计截断丢最新几轮（仅保头部，opencode 客户端压缩可部分缓解）——**v4.2 已修**：截断改「按段分配预算」（最新用户消息 16k + 尾部块）、窗口 1000 字且尾部优先并降噪、每 session 取两条快照（见 §8 v4.2）；快照均匀选取未按 session 分组的限制已由 v2 解决（见第 8 项）
7. ✅ **审计表 session 维度**（2026-09-16 晚）：迁移 234 给 prompt_audit_jobs/events 加 session_id（取自请求头 `ExtractClientSessionID` 单一入口），部分索引 (user_id, session_id, created_at)；Request→job→event 全链路穿透。注意：session_id 与 usage_logs 同源（客户端上报），历史数据为空
8. ✅ **报告链路 v2+v3**（2026-09-16/17，v3 已上线 stable）：①日报素材按 session 分组总结（`FetchPromptSnapshots`，session_id 自 9/16 17:54 起入库）——上线保留；②周报周期=**正常周一~周日**（v2 的周六周期已回滚，`StartOfWeekSaturday` 已删除），周五 20:10 定时生成覆盖周一~周五、周末工作后续手动重生成补入；③月报=**ref 所在自然月**（前端选 8 月出 8 月；调度器 1 日传上月 1 日生成上月）；④前端周报按周下拉选（近 12 周）、月报按月选（admin/user 双视图）；⑤**手动生成永不自动推飞书**（v3.1，`ReportTrigger` manual/scheduled），定时生成按类型开关+用户参与开关自动推；⑥月报聚合与当月有交集的周报（pageSize 8）
9. ✅ **飞书推送**（2026-09-17 已上线生产）：群自定义机器人 Webhook + interactive 卡片；生成成功自动推（类型开关+用户开关）+ 手动按钮（本人/admin）；commit 7c3f80740，见 §10
10. ✅ **DeepSeek Search/Fetch**（2026-11-03 已上线生产）：`POST /v1/tools/search`、`POST /v1/tools/fetch`，仅原生 DeepSeek 分组（Composite 等一律 404）；复用调度器只选 DeepSeek API Key 账号；请求/成功响应原样转发；200 且有 results/content 才按次计费（默认 0.005，可覆盖/免费/倍率），Search query 走提示词审计，见 §8 v4.0
11. ✅ **飞书多群推送**（2026-11-19 已上线生产）：新增 `report_feishu_groups` / `report_feishu_group_members` / `report_feishu_deliveries` 三表；定时与手动推送都按报告作者所属的**所有启用群组**投递（同一 webhook 去重），单群失败不影响其他群；原全局 webhook 自动迁移为「计算群」，仅当时 `report_push_enabled=true` 的 13 个用户入组；管理员在报告页「报告群组」tab 维护群与成员，见 §8 v4.1
12. ✅ **报告素材与质量闭环 v4.2**（2026-11-23 dev 已验证，2026-11-24 随 v4.3 发布 stable）：①审计截断**按段分配预算**（最新用户消息 16k + 尾部块 + 1.5k 系统提示词，静态技能清单丢弃），不再丢最近的工作；②对话窗口采样改尾部优先 + 段首对齐 + 跳过代码块；③**每 session 取「最新 + 中段」两条快照**，补回被截断丢掉的中段；④生成后确定性质量校验（小节结构/禁语/敏感信息），硬违规带反馈重写一次，仍不合格则**照发并打 ⚠️ 标记**（敏感信息例外，整篇判失败）；⑤推送幂等：已推送过的周期不再自动重复推；⑥新增 `user_text` 列，救回此前静默丢失的 8.1% 用户提问；⑦窗口降噪：按内容特征给窗口打分，先把预算让给干净窗口。见 §8 v4.2
13. ⏪ **日报附节「昨晚 20:00 后补充」v4.3**（2026-11-24 曾发布 stable，同日被 v4.4 取代）：20:00–24:00 的工作写进次日日报末尾附节；因群消息冗余，v4.4 改为滚动 24 小时窗口，附节取消。见 §8 v4.3（历史）
14. ✅ **日报滚动 24h 窗口 v4.4**（2026-11-24 已发布 stable）：日报覆盖 `[D-1 切点, D 切点)`，切点取 `DailySchedule` 时分（当前 20:00）；前晚工作直接进「今日核心工作」，**不再有附节/`annex_requests`**；统计、活跃用户、请求阈值统一用该 24h 窗口；`period_start` 保持 D 00:00（唯一键/日期筛选不变）、`period_end` 为 D 切点；见 §8 v4.4
15. ✅ **报告生成/推送失败重试 v4.5**（2026-11-24 已发布 stable）：LLM 瞬时失败每 30 分钟重试生成（定时成功后自动推、手动只更新 Web）；飞书按「报告×群」重试失败投递，已成功群/同一 webhook 不重发；有旧成功报告时保留旧报告、只补推漏掉的群；永久错误（401/403/404、key/模型/webhook 配置）标记「需人工修复」并停止重试；见 §8 v4.5
16. ✅ **提示词审计扫描器恢复**（2026-11-25）：根因 MiniMax `MiniMax-M2` 下线（404）+ base_url 缺 `/v1` + 编码套餐 key RPM 低（429）；已切 DeepSeek `https://api.deepseek.com/v1` + `deepseek-chat`，`input_limit=16000`、`worker_count=2`；新配置任务 0 失败，旧降级任务自然排空。见 §8「生产修复」
17. ✅ **报告生成任务队列 v4.6**（2026-11-25 已发布 stable）：批量生成由「串行 for 循环」改为「任务表」——每用户每周期先上账 `pending`，worker 带 8 分钟租约并发 4 路领取；`pending/done/skipped/failed/manual_repair`（`running` 由租约派生，不落库）；重试上限 6 次后转 `failed`；管理端新增「生成进度」卡片与「补跑未完成」按钮。根因：旧实现里「没轮到」的用户既没有报告也没有记录，进程重启/30 分钟预算耗尽后当天不再补跑。见 §8 v4.6

## 2. 当前部署架构（双实例，共用一套数据）

| 实例 | 端口 | 镜像 | compose 文件 | 用途 |
|---|---|---|---|---|
| `sub2api` | 33333（0.0.0.0） | `sub2api:stable` | `docker-compose.yml` | **生产**：scheduler 启用 |
| `sub2api-dev` | 33336（仅 127.0.0.1） | `sub2api:dev` | `docker-compose.dev.yml` | **开发**：`REPORT_SCHEDULER_ENABLED=false` |
| `sub2api-postgres` | 内部 | postgres:18-alpine | 主 compose | 数据库（**唯一一份**，宿主机 `./postgres_data`） |
| `sub2api-redis` | 内部 | redis:8-alpine | 主 compose | 队列/缓存（`./redis_data`） |

- 源码：`/home/xxy/fs/sub2api`，分支 `feature/daily-weekly-reports`，基点 **v0.1.184**（与线上迁移集合 273 文件零差异核实）
- 已 push 到 fork：https://github.com/CGKBAI/sub2api（branch feature/daily-weekly-reports）
- 备份：`/home/xxy/fs/backup-20260915-1124.sql`（canary 迁移前 pg_dump，70MB，历史快照；注意：2026-09-15 晚发现该文件已不在，如需快照可重新 `docker exec sub2api-postgres pg_dump -U sub2api sub2api > ...`）

## 3. Git 工作流（跟官方更新）

- `origin` = 官方 `https://github.com/Wei-Shaw/sub2api`（只 fetch，不 push）
- 本仓库改动全部在分支 **`feature/daily-weekly-reports`**（基于 v0.1.184），首个 commit：`feat: add daily/weekly user reports with prompt audit persistence`
- 项目状态文档同步提交在 `DEV-STATUS.md`（仓库根目录，与 `/home/xxy/fs/PLAN.md` 内容保持一致，每次重要变更后更新并 commit）
- **注意**：`frontend/.pnpm-store/` 是容器装依赖的产物，已加 .gitignore，勿提交

**跟官方更新**（官方出新版时）：
```bash
cd /home/xxy/fs/sub2api
git fetch origin --tags
git rebase <新tag或origin/main>          # 冲突高发点见 §7（wire/ent 生成物、挂载点文件）
# 解决冲突后需重新生成：make generate（见 §4 纪律）
docker buildx build --load -t sub2api:dev .   # 先在 33336 验证再发布
```

**推送到自己的 fork 备份**（待用户创建 fork 后执行一次）：
```bash
git remote add fork https://github.com/CGKBAI/sub2api.git   # 已配置
git push -u fork feature/daily-weekly-reports
# 以后每次 commit 后：git push fork feature/daily-weekly-reports
```

## 4. 开发循环（新 session 按此操作）

### 4.1 路径与数据

| 项目 | 路径 | 说明 |
|---|---|---|
| 源码 | `/home/xxy/fs/sub2api` | 开发分支 `feature/daily-weekly-reports` |
| 部署目录 | `/home/xxy/sub2api-deploy` | compose、`.env`、数据目录 |
| PostgreSQL 数据 | `/home/xxy/sub2api-deploy/postgres_data` | 容器内 `/var/lib/postgresql/data`（`PGDATA`） |
| Redis 数据 | `/home/xxy/sub2api-deploy/redis_data` | 容器内 `/data` |
| 应用数据 | `/home/xxy/sub2api-deploy/data` | 容器内 `/app/data` |

- dev（33336）和 prod（33333）**共用同一个 PostgreSQL/Redis**。
- dev 启动会执行迁移，所以迁移必须纯增量；dev 写的报告、配置、审计数据 prod 立即可见。
- dev 必须 `REPORT_SCHEDULER_ENABLED=false`；只有 prod 跑定时任务。

### 4.2 改代码 → dev 验证 → 发布 prod

```bash
# 1. 改代码
cd /home/xxy/fs/sub2api
git checkout feature/daily-weekly-reports

# 2. 构建 dev 镜像（镜像构建时编译前后端，有缓存约 1-2 分钟）
docker buildx build --load -t sub2api:dev /home/xxy/fs/sub2api

# 3. 重建 dev 容器
cd /home/xxy/sub2api-deploy
docker compose -f docker-compose.dev.yml up -d --force-recreate

# 4. 验证 dev（33336 仅本机）
curl -fsS http://127.0.0.1:33336/health
docker logs --tail 50 sub2api-dev
# 浏览器验证 http://127.0.0.1:33336

# 5. 发布前备份当前 stable 镜像 tag（重要）
docker tag sub2api:stable sub2api:stable-backup-$(date +%Y%m%d-%H%M)

# 6. dev 验证通过 → 给同一个镜像打 stable 标签（不是重新构建）
docker tag sub2api:dev sub2api:stable

# 7. 重建生产容器（约 20 秒中断）
docker compose up -d sub2api

# 8. 验证生产
docker compose ps
curl -fsS http://127.0.0.1:33333/health
docker logs --tail 50 sub2api
```

### 4.3 回退

```bash
# 用发布前备份的 tag 回退（日期替换为实际备份 tag）
docker tag sub2api:stable-backup-YYYYMMDD-HHMM sub2api:stable
cd /home/xxy/sub2api-deploy
docker compose up -d sub2api
```

- **不要**回退到官方 `weishaw/sub2api:latest`：会丢失审计落库修复和 reports 功能。
- 数据库迁移不会自动回滚；迁移必须纯增量，旧镜像才能继续跑在新库上。

### 4.4 纪律

- 改 ent schema / wire 后先 `docker run --rm -v /home/xxy/fs/sub2api/backend:/app -w /app -e GOFLAGS=-buildvcs=false golang:1.27.0-alpine sh -c "apk add --no-cache git >/dev/null 2>&1 && go get github.com/google/wire/cmd/wire@v0.7.0 >/dev/null 2>&1 && make generate"`，生成物提交仓库
- 编译验证**不要用管道吃退出码**：`go build ./... > /log 2>&1; echo $?`
- **前端容器操作必须钉 pnpm@9**（`corepack prepare pnpm@9 --activate`，与 Dockerfile 一致）：corepack 默认的 pnpm v10 会重写 pnpm-lock.yaml、生成 pnpm-workspace.yaml、禁用 build scripts，导致镜像构建（--frozen-lockfile）失败
- **新迁移必须纯增量**（只建新表/加可空列）：dev 启动会在共享库跑迁移，stable 共存
- **dev 和 prod 共用 `settings` 表**：在 33336 改 LLM/飞书/节假日配置会热加载到 33333；手动生成/推送会写生产 `reports` 表、发真实飞书群
- 审计 worker 双实例都消费队列（都有“必存”修复，无丢失风险）
- 不要执行 `docker image prune -a`：会删掉 `sub2api:stable-backup-*` 回退镜像

## 5. Prompt 存储事实（已核实）

- 表：`prompt_audit_events`（主）+ `prompt_audit_jobs`（队列），无自动清理，永久保留
- **格式**：`full_prompt` = 拍平纯文本（非 JSON）= [最后一条用户消息原文] + \n\n + [其余上下文（system/历史）]，段间无角色标记；超 65536 runes 时**按段分配预算**（v4.2.1 起）：最新用户消息段精确保留 **16k**（超出截断并标注）、其余按空行切块**从尾部往前**取（每块 ≤2048，只按预算停、不限块数）、开头补 1536 字符系统提示词、`</available_skills>` 技能清单整块丢弃，被跳过处插「省略 N 段」标记。拍平文本无角色标记，读取侧无法重建轮次，所以保哪些段只能在写入侧决定
- `redacted_preview` = 前 28 字符脱敏预览；`prompt_hash` = SHA256
- 元数据：user_id/user_email_snapshot/api_key_name_snapshot/model/provider/endpoint/created_at 等，`(user_id, created_at DESC)` 索引现成
- **`user_text`（v4.2 起）**：写入时抽好的「最新一条用户消息」，已剥掉 `<system-reminder>` 注入块，≤4000 rune；报告侧「用户提问记录」优先读它，历史行回退 full_prompt 头部 2000 字
- 扫描失败的事件：decision=pass + `scanner_version="scan_failed:<code>"`（降级标记，可 SQL 过滤；v3.5 起仅在重试耗尽/不可重试后降级，且前面 chunk 已成功的真实发现会聚合保留）
- 查询示例：`SELECT created_at, model, full_prompt FROM prompt_audit_events WHERE user_id=9 AND created_at >= '2026-09-15' ORDER BY created_at;`

## 6. 审计与报告配置（存 settings 表，两实例共享生效）

| settings key | 当前值 |
|---|---|
| `risk_control_enabled` | true |
| `prompt_audit_config` | 异步审计（async，不阻断），审计节点：**DeepSeek** `https://api.deepseek.com/v1` + `deepseek-chat`（2026-11-25 恢复，使用账号 id=8 的 key；AES-256-GCM 加密存 token_ciphertext，密钥=.env 的 TOTP_ENCRYPTION_KEY），input_limit=16000，worker_count=2，timeout=15000ms。**历史**：原 MiniMax `https://api.minimaxi.com` + `MiniMax-M2` 因模型下线（404）+ 缺 `/v1` + 编码套餐 key RPM 偏低导致 `prompt_guard_unavailable` |
| `report_config` | enabled=true，LLM=同 MiniMax 端点，max_prompts=60，单条截断 800 字符，日报/周报/月报 cron 均 `0 20 * * *`（仅时分生效；2026-11-24 核对生产库），skip_holidays=false（**admin 报告页页头按钮运行时切换**；开=工作日规则，关=日报每天+当日请求>10 条阈值/周报周五/月报月底出当月；存量 JSON 缺字段时 cron 读取自动补默认，代码默认 true，skip_holidays 缺省=false 需 SQL 或按钮显式写入）；飞书仅保留 `feishu_enabled` + 三类型开关，`feishu_webhook_url`/`feishu_secret` 字段保留作回滚兜底但**新代码不再读取**（群组 webhook 见 `report_feishu_groups`） |

配置修改：直接 UPDATE settings 表（ConfigManager 每 5 秒 TTL reload，无需重启）；endpoint token 必须先用 TOTP_ENCRYPTION_KEY 做 AES-256-GCM 加密（base64(nonce+ct+tag)，node crypto 可做）。

## 7. fork 相对官方 v0.1.184 的改动清单（rebase 时注意）

**新增文件**（reports 功能全套）：
- `backend/internal/domain/report.go`、`ent/schema/report.go`、`migrations/232_reports.sql`（reports 表，唯一索引 user_id+type+period_start）、`migrations/233_reports_monthly.sql`（CHECK 放宽加 'monthly'，纯增量、旧版本兼容）
- `backend/internal/service/report.go|report_service.go|report_llm.go|report_scheduler.go`
- `backend/internal/repository/report_repo.go`（usage_logs 聚合 + prompt 拉取均原生 SQL）
- `backend/internal/handler/dto/report.go`、`handler/admin/report_handler.go`、`handler/user_report_handler.go`
- 前端：`api/admin/reports.ts|api/reports.ts`（共享 `ReportType` 类型）、`views/admin|user/ReportsView.vue`（三 tab：日/周/月 + 月报 cron 配置）、i18n `zh|en/admin/reports.ts`

**DeepSeek Search/Fetch（v4.0，2026-11-03）**：
- 新增：`backend/internal/handler/openai_deepseek_tools.go`（+`_test.go`）、`backend/internal/service/openai_gateway_deepseek_tools.go`（+`_test.go`）
- 修改挂载点：`routes/gateway.go`（`/tools/search`、`/tools/fetch` + 平台 404 门）、`service/account.go`（`OpenAIEndpointCapabilityDeepSeekTools`）、`service/billing_service.go|billing_inflight_reservation.go|media_price_config.go|openai_gateway_usage.go|openai_gateway_service.go`（按平台默认价 + 按次计费/inflight）、`service/group.go`（注释）、前端 `GroupsView.vue` + i18n `zh|en/admin/overview.ts`、路由/审计覆盖测试

**飞书多群推送（v4.1，2026-11-19）**：
- 新增：`migrations/249_report_feishu_groups.sql`、`service/report_feishu_group.go`（+`_test.go`）、`repository/report_group_repo.go`、`handler/dto/report_group.go`
- 修改挂载点：`domain/report.go`（群组/投递类型 + 迁移错误）、`service/report.go`（ReportGroupRepository）、`service/report_service.go`（构造器注入 groupRepo、多群推送、列表补齐投递）、`service/report_feishu.go`（PushTestCard/pushCard 抽取）、`handler/admin/report_handler.go`（群组 CRUD/成员/测试接口、config 保存忽略脱敏占位符）、`handler/dto/report.go`、`server/routes/admin.go`、`repository/wire.go`、`service/wire.go`

**报告素材与质量闭环（v4.2，2026-11-23，2026-11-24 发布）**：
- 新增：`migrations/250_report_quality_warnings.sql`（reports.quality_warnings，纯增量）、`migrations/251_prompt_audit_user_text.sql`（prompt_audit_events.user_text，纯增量）、`service/report_quality.go`（+`_test.go`）、`service/report_material_test.go`
- 修改：`securityaudit/prompt_snapshot.go`（`buildFullPromptFromScanText` 按段分配预算 + `LatestUserTextFromScanText`）、`securityaudit/prompt_worker.go`（从 scanText 重算 FullPrompt/UserText）、`securityaudit/prompt_repository.go`（INSERT 增列）、`securityaudit/prompt_types.go`（PromptSnapshot.UserText）、`service/report_service.go`（`buildSummary` 拆为 `buildSummaryPrompt` + `completeCheckedReport`、窗口采样重写 + 降噪打分、总量预算、推送幂等）、`service/report.go`（UserPromptSnippet.CleanText）、`domain/report.go`（QualityWarnings + ErrReportSensitiveContent）、`ent/schema/report.go`（quality_warnings，含 ent 生成物）、`repository/report_repo.go`（user_text 读取 + 双快照）、`handler/dto/report.go`、前端 `api/admin/reports.ts`、`views/{admin,user}/ReportsView.vue`（⚠️ 质量提示徽标）、i18n zh/en `push.qualityWarn`

**日报附节（v4.3，2026-11-24，同日被 v4.4 取代）**：
- 新增：`service/report_annex_test.go`
- 修改：`service/report_service.go`（`reportWindows`、`reportDailyCutoff`、`reportDailyWindow`、`resolveReportWindows`、`GenerateReport`/`GenerateForAllUsers` 双窗口、`buildSummaryPrompt`/`collectReportMaterials`/`trimReportMaterials` 附节素材、`reportSystemPrompt` 附节指令）、`service/report_quality.go`（附节 regex、`splitReportAnnex`、`normalizeReportAnnex`、附节质量规则）、`domain/report.go`（`ReportStats.AnnexRequests`，JSON 字段无需迁移）、前端 `api/admin/reports.ts`、`views/{admin,user}/ReportsView.vue`（periodLabel + annexRequests 展示）、i18n zh/en（`stats.annexRequests`、`config.scheduleHint`）

**日报滚动 24h 窗口（v4.4，2026-11-24 发布）**：
- 修改：`service/report_service.go`（`reportWindows` 改为数据窗口 + 报告日期四字段、`reportDailyWindow` 返回 `[D-1 切点,D 切点)`、`GenerateReport`/`GenerateForAllUsers` 单窗口、`buildSummaryPrompt` 去掉附节素材、`reportSystemPrompt` 加“跨自然日、所有工作归入今日核心工作、不要附节”指令）、`service/report_quality.go`（删除附节校验，保留 `stripReportAnnex` 防御旧摘要/模型漂移）、`domain/report.go`（删除 `AnnexRequests`）、前端 `api/admin/reports.ts`（删 `annex_requests`）、`views/{admin,user}/ReportsView.vue`（删附节统计行）、i18n zh/en（删 `annexRequests`、`scheduleHint` 改为滚动窗口说明）、`service/report_daily_window_test.go`（由 v4.3 测试改写为滚动窗口测试）

**报告生成/推送失败重试（v4.5，2026-11-24 发布）**：
- 新增：`migrations/252_report_retry_tasks.sql`（纯增量）、`domain/report_retry.go`、`repository/report_retry_repo.go`、`service/report_retry_scheduler.go`、`service/report_retry_test.go`
- 修改：`service/report_service.go`（LLM 失败写生成重试任务、`autoPush` 语义、旧报告保留后补推漏群、`NewReportServiceWithRetry`）、`service/report_feishu_group.go`（`pushReportToMissingGroups`/`pushReportToGroup`、按群重试、同 webhook 去重、`schedulePushRetry`）、`service/report_llm.go`/`service/report_feishu.go`（typed error + 永久/瞬时分类）、`service/report.go`（`ReportRetryRepository` 接口）、`domain/report.go`（Report/Delivery 重试展示字段）、`handler/dto/report.go`、前端 `api/admin/reports.ts`、`views/{admin,user}/ReportsView.vue`（生成/推送重试徽标）、i18n zh/en `retry.*`、wire provider/cleanup（重试调度器）

**修改官方文件（挂载点）**：`config.go`（ReportConfig）、`handler.go|handler/wire.go`、`service/wire.go`、`repository/wire.go`、`routes/admin.go|user.go`、`cmd/server/wire.go`（cleanup）、`domain_constants.go`（SettingKeyReportConfig）、router/index.ts、AppSidebar.vue（ReportIcon）、i18n common.ts nav + admin/index.ts ×2、go.mod/go.sum（wire cmd indirect）

**对官方审计代码的 3 处关键修改**（`securityaudit/`）：
1. `prompt_qwen3guard.go`：scan 请求加 system prompt（让任意 OpenAI 兼容模型输出 `Safety:/Categories:` 格式）+ max_tokens 64→1024（思考型模型需要预算）
2. `prompt_worker.go`：**扫描失败也落库**（fallback result decision=pass + scanner_version 标记，Complete 强制写 event，job 记 done）——消息必存的核心
3. `report_llm.go`：输出剥离 `<think>...</think>`

## 8. 报告链路 v3 计划（2026-09-17 确定；阶段 1-2 已实施，阶段 3 进行中）

> v3 决策（用户确认）：①**周期回归正常周一~周日**（回滚 v2 阶段 2 的周六周期，删除 `timezone.StartOfWeekSaturday()`）；②**周报保持周五 20:10 定时生成**（覆盖周一~周五，默认周末不干活；周末干过活 → 之后手动重生成自动补入周六日，重生成会再次自动推飞书卡片）；③**月报语义反转**为「ref 所在自然月」（前端选 8 月 → 生成 8 月；调度器改传上月 1 日，cron 不变）；④**前端周/月选择器**：周报 tab 按周选、月报 tab 按月选；⑤v2 阶段 1 的 session 分组素材**保留不动**。

### v2 遗留状态（归档）

- ✅ 阶段 1 session 分组：已上线（commit bdcaf9050，随 9/17 飞书版 stable 发布），保留
- ⏪ 阶段 2 周六周期（`StartOfWeekSaturday`）：已上线但未被用户验证使用，v3 回滚删除
- ✅ 阶段 3 月报聚合交集周报（pageSize 8）：保留
- stable 33333 已于 9/17 随飞书功能发布（7c3f80740，包含 v2 全部代码）

### 阶段 1：后端周期调整 ✅ 已完成

- [x] 周报 `ReportPeriod` 回归 **[本周一 00:00, 下周一 00:00)**（回用 `timezone.StartOfWeek`）；周期语义注释更新（周五晚生成覆盖周一~周五 + 周末手动补漏说明）
- [x] 删除 `timezone.StartOfWeekSaturday()` + timezone_test.go 对应用例
- [x] 月报 `ReportPeriod` 反转为 **[ref 月 1 日, 下月 1 日)**；调度器月报 ref 改传 `timezone.StartOfMonth(now).AddDate(0, -1, 0)`（= 上月 1 日，保持 1 日出上月月报）
- [x] 单测重写（report_period_test.go）：周报周一边界（周五晚/周一/周日）；月报选 8 月任意日期 → 8 月周期；日报回归——全绿

### 阶段 2：前端周/月选择器（admin + user 两个 ReportsView.vue，无组件库用原生控件）✅ 已完成

- [x] 周报 tab：日期框 → **周下拉**（近 12 周，选项显示 `09.14 ~ 09.20`，value=周一日期）
- [x] 月报 tab：日期框 → `<input type="month">`（显示 2026-08，发该月 1 日）
- [x] 筛选与生成共用所在 tab 的选择值（`activeDate` computed；重叠语义天然兼容：发周一/1 日即可筛到该周/该月报告）
- [x] i18n zh/en 文案（filters.week/month）；周报 cron 默认值 `10 20 * * 5` 不变

### 阶段 3：验证与发布 ✅ 已完成（2026-09-17 晚上线）

- [x] 单测全绿 + vue-tsc EXIT=0 + go build/go vet（容器）→ buildx dev 镜像 → 33336 冒烟（HTTP 200、无 panic；ReportsView 分包含 `type:"month"` 特征确认新前端已生效）
- [x] reports 表无任何周报/月报存量记录（无周六周期数据需清理）
- [x] 用户网页验证：周报选本周（09.14~09.20）、月报选 2026-08 出 8 月月报
- [x] stable 33333 发布（HTTP 200）→ commit + push fork

### v3.1：手动生成不自动推飞书（2026-09-17 晚已上线）

> 背景：管理员点"为所有活跃用户生成"后报告全部自动推群（噪音）。用户决策：手动生成永不自动推；定时生成保持自动推（类型开关+用户参与开关不变）；推送一律可用卡片手动按钮。

- [x] `ReportTrigger`（manual/scheduled）新增于 service/report.go；`GenerateReport`/`GenerateForAllUsers` 加 trigger 参数，仅 `scheduled` 调用 `maybeAutoPushFeishu`
- [x] 调用点（编译器强制全覆盖）：scheduler → `scheduled`；admin Generate/GenerateAll → `manual`
- [x] 文案：用户页"参与飞书自动推送"提示改为"仅定时生成"；设置"自动推送类型（仅定时生成）"（zh/en）
- [x] 验证：go build/vet/test 全绿 + vue-tsc EXIT=0 + buildx + 33336 冒烟 HTTP 200
- [x] 用户验证通过：①管理员批量生成周报 → 飞书群无新消息；②点卡片"发送到飞书" → 群出现卡片
- [x] stable 33333 发布 → commit + push fork

### v3.2：报告页布局修复与主题统一（2026-09-18 已上线）

> 背景：点击侧边栏"日报周报月报"后页面无侧边栏/顶栏，只能浏览器回退。根因：本项目无全局布局（App.vue 仅裸 RouterView，每视图自行引入 AppLayout），全站 39 个视图均有 `<AppLayout>` 包裹，唯独两个 ReportsView.vue 缺失，路由切换正常但渲染成裸页面。非 window.open/新标签页问题。

- [x] `views/user/ReportsView.vue` + `views/admin/ReportsView.vue`：模板外包 `<AppLayout>`（恢复侧边栏/顶栏/背景，与全站惯例一致，参照 UsageView 写法）
- [x] 报告卡片手写类 → 主题 `card p-5`（style.css `.card`：rounded-2xl + 主题边框/阴影，跟随暗色主题变量）
- [x] 验证：vue-tsc EXIT=0 + buildx dev 镜像 + 33336 冒烟 HTTP 200 + 前端 chunk 特征确认（两个 ReportsView-*.js 均含 AppLayout/card p-5）
- [x] stable 33333 发布（healthy、HTTP 200、index hash 与 dev 一致）→ commit + push fork

### v3.3：批量生成修复 Network error（2026-09-18 已上线）

> 背景：管理员点"为所有活跃用户生成"约 30 秒后报 "Network error. Please check your connection."，仅生成 ~3 人。根因：①前端 axios 全局 30s 超时（`api/client.ts`），`generateAll` 未按惯例（参照 `admin/system.ts`）覆盖超时，每用户 LLM ~10s，3 人即触发；②浏览器断开后 gin 取消 `c.Request.Context()`，后端 `GenerateForAllUsers` 串行循环随之中断，剩余用户永不生成。

- [x] 后端 `handler/admin/report_handler.go`：`GenerateAll` 改用 `context.WithTimeout(context.Background(), 30*time.Minute)`（`reportBatchGenerateTimeout`，与 scheduler 预算一致），脱离请求 context——客户端断开不再中断批量
- [x] 前端 `api/admin/reports.ts`：`generateAll` 超时 30 分钟、`generate` 3 分钟（对齐后端 LLM 120s 上限+余量），注释说明动机
- [x] 验证：go build/vet（golang:1.27 容器）+ report 相关单测全绿；tsc 隔离检查 0 错误；buildx dev 镜像 → 33336 冒烟（healthy/HTTP 200/无 panic；admin chunk 含 `generate-all…{timeout:30*60*1e3}`、`generate…{timeout:3*60*1e3}`）→ 用户浏览器实测批量生成通过
- [x] stable 33333 发布（healthy、HTTP 200、index hash 与 dev 一致）→ commit + push fork
- 备注：构建时 alpine CDN（dl-cdn）TLS 间歇故障，临时用 sed 切 aliyun 源构建后已还原 Dockerfile；如复发可考虑固化镜像源

### v3.4：飞书推送默认关闭 + 管理员逐行开关（2026-09-18 已上线）

> 背景：飞书自动推送默认参与（迁移 235 DEFAULT true）对用户造成噪音。v3.4 决策：①默认改为不参与，存量全部回填 false；②管理员在用户管理列表逐行开关；③管理员设置后用户仍可在报告页自行双向切换。手动"发送到飞书"按钮不受开关限制（语义不变）。

- [x] `migrations/236_user_report_push_default_off.sql`：`SET DEFAULT false` + 回填 `UPDATE users SET report_push_enabled=false WHERE true`（纯增量兼容，33336 启动已验证：列默认 false、15 用户 0 true）
- [x] `ent/schema/user.go`：`Default(false)` → 容器 `go generate ./ent`（生成物 migrate/schema.go Default:false）
- [x] 后端 admin 链路（仿 `RestrictPublicGroups` 的 `*bool` 范式）：`admin/user_handler.go` UpdateUserRequest + 透传；`admin_service.go` UpdateUserInput；`admin_user.go` UpdateUser field-mask 块（repo 管道 v3.1 已有，零改动）
- [x] 前端：`UpdateUserRequest` 加 `report_push_enabled?`；`UsersView.vue` 新增隐藏列 `report_push`（列设置版本 bump 到 4，`#cell-report_push` checkbox 仿用户页样式，`handleToggleReportPush` 乐观 toast + loadUsers）；user `ReportsView.vue` fallback `?? true`→`?? false`；i18n zh/en（columns.reportPush、reportPushHint/On/Off、failedToTogglePush）
- [x] 验证：go build/vet/test 全绿 + vue-tsc 全量 EXIT=0（frontend 拷贝至 /tmp 绕开 root node_modules 后 pnpm@9 安装）→ buildx dev → 33336 冒烟（迁移生效、chunk 含 report_push_enabled/reportPushHint、无 panic）→ 用户实测（管理员列开关 ↔ 用户页双向切换）
- [x] stable 33333 发布（healthy、HTTP 200、index hash Cm7z44b8 与 dev 一致；DB 1/15 true = 测试时管理员打开的用户）→ commit + push fork
- 基建：Dockerfile 固化 aliyun apk 镜像源（dl-cdn TLS 二次复现，与 GOPROXY=goproxy.cn 同理，注释已写明）；另 vue-tsc 全量检查方法：`rsync frontend → /tmp 排除 node_modules → corepack pnpm@9 install --frozen-lockfile → node_modules/.bin/vue-tsc --noEmit`

### v3.5：调度器/审计/推送健壮性修复（2026-09-20 已上线）

> 背景：对照 §7 改动清单做全量代码审查，发现 4 个高优缺陷：①同周期重生成遇 LLM 失败会用空 summary/failed 状态覆盖原有 done 报告（数据丢失）；②调度器 last_run TTL 24h 对月报（30 天周期）错过即永久丢失、leader 锁 TTL 5min < 任务 30min 且无续期、日/周/月三种报告共享一个 30min 预算互相挤占且 last_run 先置位；③审计扫描失败立即降级落库，绕过原有 backoff 重试（瞬时 429/超时即永久免扫），且打红 prompt_worker_test 两个用例未修；④飞书推送状态零记录（失败无法补推、手动按钮无幂等依据、管理端无法审计）。调度器决策：**暂不重构对齐 ops 模式，只打最小补丁**。

- [x] `service/report_service.go` GenerateReport：LLM 失败时先 `GetByUserPeriod`，已有 done+摘要旧报告 → 保留并直接返回（日志留痕）；仅无旧报告或旧报告本身 failed 才落 failed 记录
- [x] `service/report_scheduler.go` 最小补丁：①last_run TTL 24h→35 天（覆盖月报周期，宕机恢复 catch-up 有效）；②leader 锁加 90s 续期 watchdog（compare-and-expire Lua `reportSchedulerRenewScript`，runOnce 结束停止）；③每类型独立 30min 预算（`reportSchedulerJobTimeout` ctx 移入 defs 循环）；④顺手：`fmt.Printf`→`logger.LegacyPrintf`、cron 解析失败补 error 日志、gofmt 对齐 scheduleDef
- [x] `securityaudit/prompt_worker.go` 降级语义修正：`Retryable && Attempts<MaxAttempts` 仍走 `finishFailure` backoff（5s/30s/2min）；**重试耗尽或不可重试才降级落库**；降级时若前面 chunk 已成功 → `AggregateResults` 聚合保留真实发现（warn 不丢），`ScannerVersion` 统一附加 `scan_failed:` 标记；fallback `ScannerBackend` 改 `"degraded"` + 补 `PolicyID: "priority", PolicyVersion: 1`；scanner_version 标记格式不变（§5 SQL 过滤兼容）
- [x] 测试修复 ×3：①prompt_worker_test `TestWorkerRetryBackoffTerminalFailureAndFailover`（max-attempts/invalid-terminal 改断言降级行为：NoError+completeCount 1+storePass 强制+payload 删除）；②`TestPromptAuditSyntheticAsyncBaseline`（99/100 降级返回 nil，completeCount 98→100、eventCount 8→10）；③存量 `TestOpenAICompatibleScannerRequestContract` max_tokens 断言 64→1024（v3.2 改造遗漏，审计 agent 复盘发现的第 3 个打红测试）；新增 `TestWorkerDegradesOnlyAfterRetryBudgetExhausted`（预算内重试/耗尽降级/部分成功保留发现）3 例
- [x] 迁移 `237_report_push_state.sql`：reports 加 `pushed_at TIMESTAMPTZ NULL` + `last_push_error TEXT NOT NULL DEFAULT ''`（纯增量；Update 不触碰这两列 → 推送状态跨重生成保留；33336 启动已验证列生效）
- [x] ent schema report.go：加 pushed_at（Optional+Nillable）/last_push_error 字段 → 容器 `go generate ./ent` 生成物提交；**顺带删除与唯一索引同名的冗余非唯一索引声明**——该冗余使 enttest 自动迁移建 `report_user_id_type_period_start` 报 already exists，**repository 全套测试自 232 起一直红（~40 例），本轮根因修复后首次全绿**；type 注释补 monthly
- [x] 推送状态落库：`ReportRepository.MarkPushResult(ctx,id,pushedAt,pushErr)` 新增（成功写 pushed_at 并清 last_push_error，失败仅记原因）；`maybeAutoPushFeishu`（自动）与 `pushReportWithConfig`（手动 admin/user）统一接入 `markPushResult` helper（独立 Background ctx，防调度预算/请求取消丢状态）
- [x] DTO/前端：dto Report 加 pushed_at/last_push_error（omitempty）；admin/user 报告卡片加「已推送/推送失败」徽标（title 悬浮显示推送时间/失败原因）；i18n `push.pushed/pushFailed` zh/en；`api/admin/reports.ts` Report 类型补字段（user api 复用）
- [x] 验证：go build/vet 全绿 + securityaudit 全绿 + service(Report|Feishu) 全绿 + **repository 全套首次全绿** + vue-tsc 全量 EXIT=0（/tmp 隔离 pnpm@9 流程）→ buildx dev → 33336 冒烟（healthy/HTTP 200/无 panic/迁移 237 生效/两个 ReportsView chunk 均含 pushFailed 特征）
- [x] 用户浏览器验证 33336 通过（手动推送一张卡片到飞书正常、报告页正常）→ stable 33333 发布 → commit + push fork
- 坑位记录：①容器 codegen 以 root 写文件导致后续 git 操作 Permission denied，需 `docker run --rm -v ...:/app alpine chown -R 1015:1008 /app/ent` 修属主；②`git checkout -- backend/ent` 会连手写的 ent/schema/*.go 一起还原，恢复现场后需重放 schema 编辑再重新 generate；③以 `--user 1015:1008` 跑 go generate 会写出损坏文件（GEN=1 内容错乱），**保持 root 运行 + 事后 chown** 的既定流程

### v3.6：日报「近期目标」（2026-09-20 已上线）

> 背景：用户希望在报告页自行维护近期要完成的项目目标，生成日报时体现——目标单独成节而非仅融入计划小节。决策（用户确认）：①单文本框（users.report_goal，上限 2000 rune）；②**仅日报生成时读取**，输出**三个小节**（一今日核心工作 / 二明日工作计划 / 三近期目标计划），周报/月报聚合日报摘要自然继承、不直接读取；③不做历史快照，修改后按新目标生成（重生成历史报告目标信息不保留）；④读取失败降级为无目标生成，不阻断报告。

- [x] 迁移 `238_user_report_goal.sql`：users 加 `report_goal TEXT NOT NULL DEFAULT ''`（纯增量；33336 启动已验证列生效）
- [x] `ent/schema/user.go` 加 report_goal（text Default ""）→ 容器 `go generate ./ent && go generate ./cmd/server`（注：golang:1.27-alpine 镜像无 make，§4 的 `make generate` 命令实际需直接跑两条 go generate；root 运行 + 事后 chown 流程不变）
- [x] Profile 链路（完全仿 v3.4 report_push_enabled *bool 范式，改 `*string`）：`user_handler.go` UpdateProfileRequest + 透传；`user_service.go` UpdateProfileRequest/UserUpdateFields + normalizeReportGoal（trim + 2000 rune 截断，提取 helper 可测）；`user_repo.go` field-mask；`api_key_repo.go` userEntityToService；dto types/mappers；`types/index.ts` User + `api/user.ts` updateProfile
- [x] 报告链路：`ReportRepository.GetReportGoal`（ent 单查，NotFound 返回空串）；`buildSummary` 仅 daily 且目标非空时在周期行后注入 `### 用户近期目标（用户自行填写，制定计划时必须对齐）` 小节；`reportSystemPrompt` default 分支改三小节——「## 三、近期目标计划」（1-6 条，动词开头，推进中写下一步安排/未启动写启动计划；二不再重复目标；无目标严禁输出第三节）。周报/月报分支未动
- [x] 前端 `views/user/ReportsView.vue`：页头推送开关下新增「近期目标」卡片（textarea maxlength 2000 + 保存按钮，内容未变时禁用，仿 togglePush 失败回滚）；i18n zh/en `goal.*`（placeholder 说明独立小节语义）
- [x] 测试 `report_goal_test.go` ×4（normalize trim/截断/空白；buildSummary 日报注入/空目标省略/周报不读取——fake repo + httptest 假 LLM 捕获 messages）
- [x] 验证：go build/vet + service(Report)/repository/handler 全套全绿 + vue-tsc 全量 EXIT=0（/tmp 隔离 pnpm@9）→ buildx dev → 33336 冒烟（healthy/HTTP 200/迁移 238 生效/user chunk 含 report_goal 与近期目标文案）→ 用户实测：日报重新生成出现「三、近期目标计划」小节 → stable 33333 发布 → commit + push fork

### v3.7：定时 19:00 + 法定节假日感知（2026-09-20 已上线）

> 背景：用户要求三种报告自动发送时间统一改为晚上 7 点，并贴合国家法定节假日。决策（用户确认）：①日报仅工作日生成（普通周末不发，行为变化）；②周报=每周最后一个工作日 19:00（普通周即周五；国庆前一周 9/30 出、调休补班周六可收尾）；③月报=当月第一个工作日 19:00 生成上月；④节假日数据源=内置表（每年 11 月国办公布次年安排后人工补表，无表年份回退「仅避开周末」）。

- [x] 新增 `backend/internal/pkg/holiday` 包：2026 年国办安排内置（`holidays` 休 + `workdays` 调休补班，2026-09-20 经 timor.tech 接口逐日核对）；`IsWorkday`/`HasYearData`/`LastWorkdayOfWeek`/`FirstWorkdayOfMonth`；单测 4 组（含 2027 无表回退）
- [x] `report_llm.go`：`ReportLLMConfig` 加 `skip_holidays`（默认 true）；三个 cron 默认值统一 `0 19 * * *`（仅时分生效）；normalize 空值补同默认。注意：skip_holidays 为普通 bool，存量 JSON 缺该字段 unmarshal=false，**必须靠发布时 SQL 显式写 true**
- [x] `report_scheduler.go`：cron 仍负责触发时刻（每天 19:00 评估一次），`reportDueForDay(kind, now, genMarker)` 纯函数决定生成日；last_run=评估标记（跳过日也置，防每分钟重触发），新增 `last_gen`（实际生成标记，本周/本月去重+catch-up 依据，缺失时回退 last_run 兼容升级）；无表年份 warn 一次。leader lock/90s 续期/35d TTL 全部未动
- [x] admin handler `updateReportConfigRequest` 加 `skip_holidays`（*bool 透传，redact 值拷贝自动带出）
- [x] 前端：admin 设置弹窗加「规避法定节假日」开关+说明块；cron 三输入框标签改「仅时分生效」；`autoPushHint` 时间文案更新（用户页复用同一 i18n key）；i18n zh/en
- [x] 测试：`report_scheduler_rule_test.go` 20 例（工作日/调休/整周全假/catch-up/去重/月报顺延含 1/4 元旦补班出上月）全绿
- [x] 发布顺序关键：**先 stable 镜像后 SQL**（旧代码吃到 daily-fire cron 会天天生成）；SQL `value=(value::jsonb||'{...}')::text` 写入新 cron ×3 + skip_holidays=true，5s 热加载；Redis 预置 `last_gen:weekly=9/18 20:10`、`last_gen:monthly=9/1 20:20`（旧 last_run 已在 24h TTL 时代过期，否则升级当晚周报/月报会重复补发）
- [x] 验证：go build/vet + holiday 全部 + service Report 全绿 + vue-tsc EXIT=0（/tmp 隔离 pnpm@9）→ buildx dev → 33336 冒烟（healthy/HTTP 200/无 panic/二进制含新逻辑与 i18n key）→ stable 33333 发布（healthy/HTTP 200）→ SQL 生效（4 字段确认）→ commit + push fork
- 上线当日语义核对（2026-09-20 周日调休补班）：19:00 日报生成+推送；周报/月报 skip 留日志；下个节点=周报 9/24（周四，9/25 中秋）、9 月月报 10/8（国庆后首个工作日）

### v3.8：节假日感知页头开关 + 关模式语义（2026-09-20 晚已上线）

> 背景：用户要求把节假日感知做成管理员按钮，并明确关闭时的语义。决策：**开**=v3.7 工作日规则不变；**关**=日报每天发（仅当日请求 >10 条的用户，定时路径）、周报固定周五（错过当晚可在周末补发）、月报每月最后一天出当月（不跨月补，可手动补）。

- [x] `report_scheduler.go`：`reportDueForDay` 加 `skipHolidays` 参数双模式分支；关模式周报=本周五 0 点起 + genMarker 本周去重/catch-up（与开模式同构），月报=`isLastCalendarDayOfMonth`（**不查 genMarker**——开模式 marker 表示"上月已出"、关模式表示"当月已出"，语义冲突，跨模式切换当月可能重复出一次月报，已知边缘）；月报 ref 模式相关：开=上月 1 日、关=now（月底出当月）
- [x] 日报阈值：`reportDailyMinRequests=10` + 纯函数 `belowDailyMinRequests`（仅 scheduled+daily+关模式+≤10 生效），`GenerateReport` 在 stats 后返回哨兵 `ErrReportSkippedLowUsage`，`GenerateForAllUsers` 循环静默跳过；手动生成/周报/月报不受限
- [x] 前端：admin ReportsView 页头新增「节假日感知：开/关」按钮（onMounted getConfig 初始化，点击 updateConfig 单字段切换，title 悬浮显示两模式规则）；设置弹窗移除 checkbox 改为 `scheduleHint` 静态说明（cron 仅时分生效）；`autoPushHint` 改模式中性文案；i18n zh/en（actions.holidayAwareOn/Off/Hint/holidayToggled + config.scheduleHint）
- [x] 测试：规则用例扩到 33（关模式 12 例：周末/节假日日报照发、周五、错过补发、去重、9/30 与 2/28 月底、不跨月补）+ `belowDailyMinRequests` 7 例全绿
- [x] 验证：go build/vet/test + vue-tsc EXIT=0 → buildx dev → 33336 冒烟（healthy/HTTP 200/无 panic/admin chunk 含 holidayAwareHint）→ stable 33333 发布（healthy/HTTP 200；无 SQL——开关由按钮运行时切换，生产当前=开）→ commit + push fork

### v3.9：周报/月报发布日抑制日报推送（2026-09-20 晚已上线）

> 背景：用户反馈周报/月报与日报同时发布导致飞书群消息过多。决策：当天若发布周报或月报，日报**照常生成**但**不自动推飞书**（手动按钮推送不受影响）。

- [x] `report_service.go`：`GenerateReport`/`GenerateForAllUsers` 加 `suppressAutoPush bool` 参数（仅 scheduled 路径生效，手动调用一律 false）；抑制时不调 `maybeAutoPushFeishu`
- [x] `report_scheduler.go`：抽出 `evaluateKind(kind, spec, now, skipHolidays)` 纯读取方法（cron 到期 + 生成日规则，可重复调用）；runOnce 先预判周报/月报今天是否发布（`biggerReportToday`），再逐类型生成，日报在发布日带 suppress 并打一条 `daily auto push suppressed` 日志；last_run/last_gen 语义不变（评估标记在 cronDue 后置位）
- [x] handler 两处手动调用点补 `false`；前端无 UI 变化，仅 `autoPushHint` 文案补充抑制规则说明（zh/en）
- [x] 验证：go build/vet/test + vue-tsc EXIT=0 → buildx dev → 33336 冒烟（healthy/无 panic/二进制含抑制日志特征）→ stable 33333 发布 → commit + push fork
- 注：抑制条件=「周报/月报当天定时发布」；用户级 report_push_enabled、类型开关等原有条件不变；抑制只作用于自动推送，报告生成与推送状态落库（pushed_at 不写）不受影响

### v4.0：DeepSeek Search / Fetch 独立工具接口（2026-11-03 已上线）

> 背景：DeepSeek 提供独立的 Search（网页搜索）与 Fetch（URL 抓取）HTTP API，需要作为网关工具接口透传并按成功调用计费。决策（用户确认）：仅原生 DeepSeek 分组可用；请求体原样转发；只有真正产生结果的调用计费；空结果与失败不计费；默认 0.005/次并复用 `web_search_price_per_call` 覆盖机制。

- 路由：`POST /v1/tools/search`、`POST /v1/tools/fetch`（`routes/gateway.go`）；非 DeepSeek 分组（含 Composite）统一 404——Composite 请求体没有 model 字段可路由，且 handler 双重校验原始分组平台
- 调度：新增 `OpenAIEndpointCapabilityDeepSeekTools`，`SupportsOpenAIEndpointCapability` 仅允许 `PlatformDeepseek + AccountTypeAPIKey`；复用 `SelectAccountWithSchedulerForCapability`，上游 URL = 账号 `GetOpenAIFormatBaseURL()` + `/v1/tools/{search,fetch}`，请求体、成功响应状态/响应体/主要响应头原样返回
- 错误：401→账号置错误；402→余额不足临时停调；429→冷却；408/429/5xx→换账号重试（最多 3 次）；其他 4xx→原样返回上游错误，不重试
- 计费：仅 HTTP 200 且 Search `results` 非空 / Fetch `content` 非空时记 1 次 `WebSearchCalls`；空结果仍返回 200，但不落用量日志；用量 model 为 `deepseek-search` / `deepseek-fetch`，默认 0.005/次（`defaultDeepSeekToolPricePerCall`），分组 `web_search_price_per_call` 可覆盖（0 免费），叠加分组倍率；inflight 预估同步接入
- 审计：Search 的 `query` 复用现有提示词审计（OpenAI Chat 协议），Fetch 仅 URL 不做文本提示词审计；路由覆盖测试将 `/tools/search` 归入审计、`/tools/fetch` 归入排除
- 管理端：DeepSeek 分组显示「搜索与抓取计费」，占位/预览默认 0.005；OpenAI 分组维持 0.01
- 测试：新增 service/handler/route 单测（能力筛选、字节级转发、空结果不计费、错误映射、计费默认/覆盖/免费、平台隔离）；go build/vet + service/handler/routes 全量单测全绿；vue-tsc EXIT=0
- 发布：dev 镜像 → 33336 冒烟（健康、路由注册、二进制含新前端文案）→ stable 33333 发布（无新迁移）

### v4.1：飞书多群推送（2026-11-19 已上线）

> 背景：日报目前只发一个飞书群，但有人身兼多个部门，其报告也需要发到其他部门群。决策（用户确认）：①按「每人日报各推各群」投递（不是部门汇总卡）；②新增报告群组管理页维护群 + 成员；③现有 webhook 迁移为普通群组「计算群」，当时已勾选自动推送的用户入组；④手动按钮发到报告作者所属所有群；⑤每个群独立记录投递结果；⑥日/周/月都发，沿用全局类型开关。

- 迁移 249：`report_feishu_groups`（群名唯一 + webhook/secret/enabled）、`report_feishu_group_members`（多对多）、`report_feishu_deliveries`（report×group 唯一 + attempt_count）；纯增量，旧镜像兼容。初始化从 `settings.report_config` 复制 webhook/secret 建「计算群」，把 `report_push_enabled=true` 的 13 个用户加入
- 推送路由：`pushReportToUserGroups` 查用户所属启用群组 → 按 webhook URL 去重 → 最多 4 并发、单群 10s、总预算 30s → 每个群独立 UPSERT 投递结果；单群失败不阻塞其他群；全成功才更新 `reports.pushed_at`，部分失败写 `last_push_error`（含群名与原因）
- 自动推送条件不变：全局 `feishu_enabled` + 类型开关 + 用户 `report_push_enabled`；用户无群组时静默跳过
- 手动推送：`PushReport`/`PushUserReport` 改为发到报告作者所属所有启用群组，不检查类型/用户开关（沿用原手动语义），无群组返回 `REPORT_FEISHU_NO_GROUPS`
- 管理端：`/admin/reports/groups` CRUD + `PUT .../members` + `POST .../test`；webhook/secret 返回脱敏占位符、留空或回传占位符保留旧值；admin 报告页新增「报告群组」tab（列表/编辑/测试/删除、成员多选搜索）
- 前端：报告卡片徽标显示「已推送 x/y 群」「部分失败 x/y 群」，悬浮显示失败群原因；设置弹窗移除单 webhook/secret 输入，仅保留总开关 + 三类型开关
- 测试：新增 service 多群路由单测（去重、部分失败隔离、无群组、opt-in/类型开关、测试卡片、创建校验）；go build/vet + service/repository/handler/routes 全绿；vue-tsc EXIT=0；dev 33336 迁移后核对「计算群」13 人、投递表为空；stable 33333 发布 healthy、HTTP 200、前端 chunk 含新逻辑
- 发布顺序：dev 启动先在共享库跑迁移 → 验证 → 备份 `sub2api:stable-backup-20261119-1459` → `sub2api:dev` 打 stable → 重建生产；旧 settings webhook 保留，可回滚

### v4.2：报告素材与质量闭环（2026-11-23 dev 已验证，2026-11-24 随 v4.3 发布 stable）

> 背景：对照 §8 审查遗留，修四类 P0 缺陷。它们共同决定"报告内容对不对"——素材进不来正确的东西、垃圾条目没人拦、同周期还会重复刷群。
> 决策（用户确认）：①截断配额取 **头 8k + 尾 56k**；②质量不合格时**照发 + ⚠️ 标记**（不扣住，漏报比质量差更伤信任），敏感信息例外。

- [x] **1. 审计截断改头尾保留**（`securityaudit/prompt_snapshot.go`）：新增 `TrimRunesHeadTail`，`BuildFullPrompt` 超 65536 runes 时保 **头 8192（本轮用户消息）+ 尾 ≈57k（最近上下文）**，中间插 `……（此处省略 N 字）……` 标记，总长仍 ≤ limit（存储上界不变）。为什么只能在写入侧修：`full_prompt` 是段间无角色标记的拍平文本，读取侧无法重建轮次。**只影响新数据**，历史行不动
- [x] **2. 对话窗口采样重写**（`service/report_service.go`）：`conversationWindowRunes` 700→1000、`conversationWindowBudget` 32→48（单日素材上限约 4.8 万字符）；`sampleWindows` 改为 ①**尾部优先**（后 30% 对话区分到一半窗口）②**段首对齐**（起点吸附到最近段落/代码块边界，最多 240 rune）③**跳过 ``` 围栏内窗口** ④锚点 `</available_skills>` 缺失且文本超长时先丢前 8192 rune；尾段容纳不下时预算回填前段，保证总窗口数稳定；完全相同的窗口去重
- [x] **2b. 素材预算改为按段分配**（替代盲切头尾，2026-11-23 追加）：`buildFullPromptFromScanText` 用优先级分隔符**精确定位**「最新用户消息」段并单独给 **16k**（用户粘贴的超长日志只留前 16k）；其余内容按空行切块后**从后往前**取（每块 ≤2048，只按预算停、不限块数）；开头补 1536 字符系统提示词用于识别客户端/场景；命中 `</available_skills>` 的静态技能清单**整块丢弃**（实测 44% 的超长请求里它在吃头部预算）；被跳过处插「省略 N 段」标记。handler 与 worker 两条写入路径统一调用同一函数（worker 原本会用 scanText 覆盖 handler 的结果，只在 handler 改是无效的）。坑：NUL 剥离必须在切分优先级分隔符**之后**——分隔符本身含 `\x00`
- [x] **2c. 每 session 取 latest + 中段两条快照**（`repository/report_repo.go`）：`FetchPromptSnapshots` 由「每 session 只取最后一条」改为取两条——latest（最近一条）+ mid（会话时间中点之前最近一条）。因为每条请求都是"会话至今"的**累积快照**，mid 的尾部恰好覆盖 latest 因 64k 截断丢掉的中段，等于用零写入成本把当天覆盖面翻倍
- [x] **2d. `prompt_audit_events.user_text`（P2，迁移 251）**：写入时直接把「剥掉 `<system-reminder>` 注入块后的最新用户消息」（≤4000 字）单独存一列，报告侧优先读它、历史行 `COALESCE` 回退到 full_prompt 头部 2000 字。**解决实测 8.1% 的提问静默丢失**（旧逻辑在头部 2000 字内遇到未闭合注入块就判为空）。未闭合块只去标签、保留其后内容以尽量救回输入；`CleanText=true` 的片段跳过正则二次提取（避免 "#"/"{" 开头被噪音启发式误杀）。写入点在 worker 里从 scanText 重算（job 行不存 user_text，不需要改 job 表）
- [x] **2e. 窗口降噪（P4，2026-11-23）**：拍平文本没有 role 标记，读取侧拿不到「这段是 tool 还是 user」，所以用**内容特征做等效降权**——`reportWindowNoise` 按标点密度、超长行+低中文占比、基本纯英文长文本、diff/堆栈/npm 等特征、同行重复 ≥4 次打 0~1 的噪音分；采样改为**先按 1.5 倍预算抽候选**，再 `selectReportWindows` 按噪音从高到低裁回预算（噪音相同时优先淘汰更早窗口，保住"最近的工作"），输出仍保持时间顺序。**降噪不减少素材总量**，只是把预算让给更干净的窗口；纯中文叙述的窗口得 0 分、不会被误伤
- [x] **3. 上下文总量预算**（`buildSummaryPrompt`）：新增 `reportUserPromptMaxRunes = 80000`，固定段（周期/目标/子报告/统计）优先，剩余额度在"提问记录"与"对话片段"间**交替从最早一条开始丢**；`max_prompts` 死配置接线（此前一直用硬编码 300），按"去重后最多注入多少条有效提问"生效、超出保留最近
- [x] **4. 生成后质量校验**（新增 `service/report_quality.go`）：`checkReportQuality` 分三级——**硬违规**（小节结构缺失/多余第三节/禁语"次数·工具·模式"/敏感信息）、**软问题**（核心工作条目数、条目 >60 字、代码块/表格、空泛措辞，**仅记日志不打徽标**，避免多数正常报告都带 ⚠️）、**敏感**；硬违规把清单回灌**重写一次**，仍不通过则选更轻的一份**照发**并写 `quality_warnings`；敏感信息在两次输出中都出现时整篇判失败（`ErrReportSensitiveContent`，不落库不推送）。另加 `sanitizeReportBody` 自动剥掉 LLM 多输出的 H1 标题
- [x] **5. 推送幂等**（`maybeAutoPushFeishu`）：`pushed_at` 非空即跳过自动推送并记日志。`persist` 的 Update 不触碰 `pushed_at`，所以重生成同周期报告时该字段仍是真实推送状态——Redis 标记丢失、leader 切换、节假日模式切换导致的"同周期再生成一次"不再重复刷群；补发/更新走卡片手动按钮（与 v3.1 语义一致）
- [x] **6. 落库与前端**：迁移 250 加 `reports.quality_warnings`（纯增量）；domain/ent/DTO/仓储全链路；admin/user 报告卡片在推送徽标后新增 `badge-warning`「⚠️ 质量提示」徽标，悬浮显示具体问题；i18n zh/en `push.qualityWarn`
- [x] 测试：新增 `report_quality_test.go`（结构调整/禁语/敏感/第三节目/软问题/标题剥离）、`report_material_test.go`（尾部优先/段首对齐/代码块/总量预算/max_prompts）、`TestMaybeAutoPushFeishuSkipsAlreadyPushedReport`、`TrimRunesHeadTail` 用例、窗口降噪打分/选择用例（`TestReportWindowNoiseClassifiesToolOutput`、`TestSelectReportWindowsDropsNoisiestKeepingOrder`）；`TestCollectConversationWindowsBudget` 按新预算更新（预算不再被突破：40 session → 48 窗）
- [x] 验证：`go build ./...` + `go vet`(service/repository/securityaudit/handler) 全绿；`go test` service/securityaudit/handler 全绿；`vue-tsc --noEmit` EXIT=0
- [x] **线上实测**（dev 33336，用真实流量核对新格式）：32 条新格式事件平均 **53.9k 字符**、p50 **65,536**（预算用满）、无一条塌缩到 5k 以下、`available_skills` 残留 1/22；`FetchPromptSnapshots` 实测 8 个 session → 16 行（latest+mid）
- [x] **验证中发现并修复两个真实缺陷**（改前单元测试全绿也看不出来）：①尾部取块时"最后一块太短"直接 `break`，导致整段尾部被丢弃、素材塌缩到 **1.9k 字符**——改为 `continue` 继续往前找；②尾部块数硬限 24 块，而块平均只有几百字，**65k 预算只用掉 16k**——改为只按预算停
- [x] **dev 验证已完成**（2026-11-23）：迁移 250/251 已在共享库生效（纯增量、旧镜像兼容），33336 health 200、无 panic/ERROR，前端 chunk 含质量徽标，真实流量核对存储格式与 user_text
- [x] **已发布 stable（2026-11-24）**：v4.2 与 v4.3 一起发布；备份 `sub2api:stable-backup-20261124-1315` → `sub2api:dev` 打 stable → 重建 33333 → healthy、`/health` 200、前端 index hash 与 dev 一致；commit `84adf1144` 已 push fork

**已知未做（明确留到后续，不阻塞本次发布）**：
- **报告里显示「素材覆盖率」**：生成时记下"有效素材字符 / 原始上下文字符"，前端/卡片展示，让人自己判断报告可信度。属于体验增强，不是缺陷。
- ~~**审计扫描器故障 `prompt_guard_unavailable`**~~：**2026-11-25 已恢复**，根因是 MiniMax `MiniMax-M2` 模型下线（404）+ base_url 缺 `/v1` + 编码套餐 key 的 RPM 偏低（429）；已切 DeepSeek `deepseek-chat`、`input_limit=16000`、`worker_count=2`。见 §8「生产修复：提示词审计扫描器恢复」。

### v4.3：日报附节「昨晚 20:00 后补充」（D 方案，2026-11-24 曾发布 stable，已由 v4.4 取代）

> **已被 v4.4 取代（2026-11-24）**：日报改为滚动 24 小时窗口，附节取消；以下保留历史记录。
>
> 背景：生产日报在 20:00 生成（库内 `report_config` 当前 `0 20 * * *`、`skip_holidays=false`），当天 20:00–24:00 的使用不会进当晚报告，也不会自动滚进次日。实测近 7 天 11-18 有 24.7%、11-22 有 66.7% 的请求发生在 20:00 后；11-22 当晚 3 份日报都是 20:00 生成的，那晚大部分工作不在里面。
> 决策（用户确认，D 方案）：**20:00 照发主日报；20:00 后的工作写进次日日报末尾的附节**，不额外发卡片、不延迟主日报。
> 核心口径：以日报 cron 的时分（当前 20:00）为硬切点。报告日 D 的正文覆盖 `[D 00:00, D 20:00)`；附节覆盖 `[D-1 20:00, D 00:00)`。两个窗口在 D 20:00 生成时都已闭合，补跑/重生成不会产生时间漂移，也不会和正文重叠。

**格式（固定；附节只在有有效素材时出现）**

```markdown
# 工作日报（2026-11-24）- 谢翔宇

## 一、今日核心工作
1. 完成报告附节数据链路改造并补充单测
2. 修复报告页在跨时区下的日期筛选偏差
3. 评审群组推送重试方案并给出落地建议

## 二、明日工作计划
1. 联调附节生成到飞书卡片的端到端流程
2. 补齐附节的质量校验用例

## 三、近期目标计划            ← 仅当用户填写了近期目标
1. 推进报告链路 v4.3 在 dev 验证后发布 stable

## 附：昨晚 20:00 后补充      ← 时分跟随日报 cron；当前 20:00
1. 完成审计表 user_text 回填脚本并在预发验证
2. 处理线上导出任务空数据报错，已合并主分支
```

附节规则：
- **位置**：全文最后；有第三节时在其后，无第三节时在第二节后。标题形如 `## 附：昨晚 HH:MM 后补充`，`HH:MM` 取日报 cron 时分（当前 20:00），**不写死**，改 cron 时自动跟随。附节**不参与「一/二/三/四」编号**，避免用户没填近期目标时出现「三、四」跳号。
- **条数/长度**：1–3 条，按重要性排序；每条 ≤30 字（与 system prompt 现有要求一致），动词开头。
- **内容**：只写昨晚 20:00–24:00 的实际工作；不重复「今日核心工作」；写结果或推进到哪一步，不写工具、模式、请求次数、token/费用等统计数字。
- **无有效素材**：整节省略，不输出「无」「暂无」占位。
- **锚点**：「昨晚」以报告日期 D 为锚点，不是生成日期；补生成历史日报时语义不变。
- **敏感信息**：与正文同规则，命中且重写后仍在 → 整篇失败（不落库不推送）。
- **飞书**：附节随次日日报同一张卡片推送，`##` 由现有 `report_feishu.go` 转成粗体行；不额外发卡片。

**数据与生成口径**

- 主素材查询 `[D 00:00, D 20:00)`；附节素材查询 `[D-1 20:00, D 00:00)`；全部半开区间，`20:00:00` 整点归附节、`19:59:59` 归正文。
- 同一次 LLM 调用：prompt 增加「前一日 20:00 后素材」块；只有存在有效素材时才注入该块和附节指令。有效素材 = 去噪后至少 1 条用户提问或 1 个对话窗口；只有 usage 统计、没有可读内容时不出附节。
- 素材预算：**主素材维持 `reportUserPromptMaxRunes=80000` 不变**，附节单独封顶 20000（总上下文上限约 100k 字符，避免为了附节压缩正文素材）；附节素材同样按 session 分组、尾部优先、降噪。若实际模型上下文吃紧，再降附节预算到 12k。
- 统计口径：卡片/图表的 `stats` 仍只统计正文窗口 `[D 00:00, D 20:00)`，附节是叙述性补充，不并入请求数/token/费用，避免同一晚数据在两天统计里重复计数。`ReportStats` 增加 `annex_requests`（JSON 字段，无需迁移），卡片统计行显示「另含前晚 20:00 后 N 条」；附节内容不进图表。
- 周期字段：日报 `period_start` 保持 D 00:00（唯一键/筛选不变），`period_end` 改为 D 20:00，**只描述正文/统计窗口**；附节是跨周期补充，不改变 period 字段。前端日报标签显示 `2026-11-24（截至 20:00）`，周/月标签不变。旧报告不改。
- 活跃用户筛选：日报改为「正文 ∪ 附节」窗口的并集，避免只在 20:00 后用过、次日没再用的人拿不到报告；`stats.Requests==0` 但附节有素材时仍生成（主统计为 0）。
- 请求阈值：关模式下 `>10 条` 改用正文+附节合计，避免 20:00 后才补够量的用户被跳过。
- 手动生成：与定时同口径，选日期 D 生成正文 `[D 00:00,D 20:00)` + 附节 `[D-1 20:00,D 00:00)`；当天 20:00 前手动生成时正文只到生成时刻（重生成会覆盖修正）。
- 补跑：20:00 后恢复仍只生成到 20:00 的正文，20:00 之后归次日附节；不再把恢复时刻前的数据塞进当天正文。
- 幂等/推送：附节属于同一份 `reports` 记录，`pushed_at` 语义不变；次日日报推送失败仍走手动补发。
- 周报/月报：按日报摘要聚合，附节随次日日报进入次日所在周期；跨周/跨月的最后一晚可能计入下一周期（如周日 20:00 后进入下周一日报）。v1 不做跨周期回算，作为已知边界。
- 节假日模式边界：附节依赖「次日有日报」。`skip_holidays=true` 时周末/节假日次日不生成日报，该晚 20:00 后的数据不会被附节覆盖（仍按现有「周末手动重生成」处理）；当前生产 `skip_holidays=false`，不受影响。
- 历史数据：只影响新生成的报告；旧报告不自动加附节，重生成时按新口径。v1 不加数据库列（附节是 `ai_summary` 的一部分）。

**改造清单（已完成，2026-11-24 发布 stable）**

- [x] 新增 `resolveReportWindows`/`reportDailyWindow`/`reportDailyCutoff`：日报正文 `[D 00:00, D 切点)`、附节 `[D-1 切点, D 00:00)`，cutoff 取 `DailySchedule` 时分；周/月沿用 `ReportPeriod`
- [x] `GenerateReport` 日报路径分别取两组素材；主素材预算 80k 不变，附节独立 20k；`reportSystemPrompt` 日报分支加附节格式与规则
- [x] `report_quality.go` 识别 `## 附：昨晚 HH:MM 后补充`（时分取配置）：有素材但缺失 → 硬违规重写；无素材却出现 → 生成后剥除；附节条目 1–3/≤30 字软校验；附节不参与「三/四」编号判断
- [x] `splitReportAnnex`/`normalizeReportAnnex` 兼容附节标题（含 `**附**`/`### 附` 归一化）；前端 Markdown 渲染无需改动
- [x] 日报活跃用户筛选改为正文∪附节并集；`belowDailyMinRequests` 用正文+附节合计；`stats.Requests==0` 但有附节素材时允许生成
- [x] 日报 `period_end` 改切点时间；前端日报 periodLabel 显示 `2026-11-24（截至 20:00）`，旧报告只显示日期；周/月不变
- [x] admin 配置提示补日报切点说明；报告卡片统计行显示「另含前晚切点后 N 条」（`annex_requests`，JSON 字段无需迁移）
- [x] 单测：`report_annex_test.go`（19:59:59/20:00:00 半开边界、切点解析回退、附节素材注入、附节质量规则、附节归一化/剥除）；service 全量测试、`go build`、`vue-tsc` 全绿
- [x] dev 33336：buildx 构建 `sub2api:dev` → 重建容器 → `/health` 200、无 panic/ERROR；前端二进制已含 `annexRequests`/`截至` 新逻辑
- [x] **真实流量生成验证**（dev 33336 admin 手动，user 7 / 2026-11-20）：正文 5 条、附节 33 条，与 `usage_logs` 两窗口计数完全一致；附节内容命中 11-19 20:00–24:00 prompt（IdeaToDoc/宣传片）；`period_end=20:00`、`pushed_at` 为空（手动不自动推）；质量校验无警告；验证后已删除测试报告 854
- [x] 补跑切点单测（22:00 补跑正文仍切 20:00）与飞书附节加粗转换单测
- [x] **发布 stable（2026-11-24）**：备份 `sub2api:stable-backup-20261124-1315` → `sub2api:dev` 打 stable → 重建 33333 → healthy、`/health` 200、前端 index hash 与 dev 一致（`index-DESiiRsY.js`）；commit `84adf1144` 已 push fork
- [ ] 定时 20:00 自动推送实跑/飞书群实际卡片待 2026-11-24 20:00 后观察（上线后首个定时周期）

**已定取舍（不再讨论）**

- 标题用 `附：`，不用 `四、`：编号会随「有没有近期目标」漂移。
- 附节统计不并入主 `stats`：同一晚数据不能在两天重复计数；需要展示就单列 `annex_requests`。
- v1 不做跨周/跨月回算：先保证「不丢」，周期归属边界后续再优化。

### v4.4：日报滚动 24 小时窗口（2026-11-24 已发布 stable）

> 背景：v4.3 的附节让群卡片多出一块，用户反馈冗余。决策：日报改为**滚动 24 小时窗口** `[D-1 切点, D 切点)`，前晚工作直接进「今日核心工作」，**取消附节**；切点仍取 `DailySchedule` 的时分，不写死。
> 核心口径：报告日 D 的日报覆盖前一天切点至当天切点；`period_start` 仍是 D 00:00（唯一键/日期筛选不变），`period_end` 是 D 切点；统计、活跃用户、请求阈值统一使用这一个 24h 窗口。

**格式（仅这三个小节，不再有附节）**

```markdown
# 工作日报（2026-11-24）- 谢翔宇

## 一、今日核心工作
1. 完成报告窗口改造并补充单测
2. 修复报告页跨时区筛选偏差
3. ...

## 二、明日工作计划
1. ...

## 三、近期目标计划
1. ...
```

**实现要点**

- `reportWindows` 拆为数据窗口 `Start/End` 与报告日期 `ReportStart/ReportEnd`；日报 `Start=D-1 切点`、`End=D 切点`、`ReportStart=D 00:00`、`ReportEnd=D 切点`
- `GenerateReport`/`GenerateForAllUsers` 只取一个窗口；不再做附节素材、`annex_requests`、正文∪附节并集、阈值合计
- `buildSummaryPrompt` 只注入一个窗口的素材；`reportSystemPrompt` 明确「窗口可能跨自然日，所有工作归入今日核心工作，不要另起附节/昨晚小节」
- `report_quality.go` 删除附节校验；保留 `stripReportAnnex` 防御旧日报摘要带来的 `## 附` 或模型漂移
- 前端删除「另含前晚 N 条」统计行和 `annex_requests` 类型；日报 `period_end` 仍是 D 切点，标签继续显示「D（截至 20:00）」
- 周报/月报保持自然周/自然月口径不变（已知边界：周一日报会包含周日晚上，聚合时归属下一周期）

**验证与发布（2026-11-24）**

- 后端 `go build ./...`、`go vet`、`go test ./internal/service/...` 全绿；前端 `vue-tsc`、i18n 完整性测试通过
- dev 33336 真实流量生成（admin 手动，user 7 / 2026-11-20）：请求 **38** 条（前晚 33 + 当天 5），与 `usage_logs` 24h 窗口一致；正文含前晚 `IdeaToDoc` 工作；**无 `## 附`、无 `annex_requests` 字段**；`pushed_at` 为空；验证后删除测试报告 855
- 发布：备份 `sub2api:stable-backup-20261124-1522`（v4.3 stable）→ `sub2api:dev` 打 stable → 重建 33333 → healthy、`/health` 200、前端 index `index-DUQ2agLp.js` 与 dev 一致；commit `97d456534` 已 push fork
- [ ] 2026-11-24 20:00 首个定时周期观察：日报生成、自动推送、卡片无附节、窗口为 11-23 20:00 ~ 11-24 20:00

### v4.5：报告生成/推送失败重试（2026-11-24 已发布 stable）

> 背景：LLM 瞬时失败会导致部分用户没有日报/推送，只能人工生成再手动推；飞书推送失败也没有自动重试。用户确认规则：生成和推送失败都 30 分钟重试；有旧报告保留；已成功的人/群不重复；永久错误标记「需人工修复」并停止。

**规则**

- **生成失败，无旧报告**：写 `failed` 报告 + 生成重试任务，每 30 分钟重试；定时成功后自动推，手动成功后只更新 Web。
- **生成失败，已有成功旧报告**：保留旧报告，不重新生成；定时报告把旧报告补推到还没成功的群；已成功的群/同一 webhook 跳过。
- **推送失败**：按「报告×群」重试，每 30 分钟一次；已成功群和同一 webhook 不重发；全部群成功才写 `pushed_at`。
- **手动推送失败**：仍不自动重试（沿用“手动按钮”语义），只落失败记录。
- **永久错误**：LLM `400/401/403/404/422`、飞书 `4xx`/业务码（`9499` 限流除外）→ `manual_repair`，停止重试；网络超时、`429`、`5xx` 继续重试。
- 生成重试任务成功后，若用户/类型开关关闭或无群组，则不再推送。

**实现**

- 迁移 `252_report_retry_tasks.sql`：新表 `report_retry_tasks`，`kind=generation|push`，`status=pending|manual_repair|done`，`next_retry_at`，部分唯一索引保证同一用户周期/同一报告群只有一个未结束任务。
- `ReportRetrySchedulerService`：每分钟扫描到期任务，失败后 `next_retry_at=now+30min`；Redis leader lock；受 `REPORT_SCHEDULER_ENABLED` 控制（dev 不跑）。
- LLM/飞书错误改为 typed error：`reportLLMCallError`、`reportFeishuError`，保留 HTTP/业务码用于永久/瞬时分类。
- 推送按群重试：`pushReportToMissingGroups`（自动推送只补缺失群）、`pushReportToGroup`（单群重试）；同一 webhook 已成功则只标记该群成功，不重复发卡片。
- 前端报告卡片：新增「生成重试中/生成需人工修复」徽标；推送失败 tooltip 显示「推送重试中/需人工修复」。

**验证与发布（2026-11-24）**

- `go build ./...`、`go vet`、`go test ./internal/service/...` 全绿；前端 `vue-tsc`、i18n 完整性测试通过。
- dev 33336 启动执行迁移 252，`report_retry_tasks` 表/索引/约束正确；手工事务验证 generation/push UPSERT 与部分唯一索引。
- 临时插入 generation/push 任务，admin 报告列表正确返回 `generation_retry_status`/`retry_status`；验证后删除，表恢复为空。
- 发布：备份 `sub2api:stable-backup-20261124-1735`（v4.4 stable）→ `sub2api:dev` 打 stable → 重建 33333 → healthy、`/health` 200、前端 index `index-DUDskspr.js` 与 dev 一致；生产日志出现 `[ReportRetry] scheduler started (tick=1m, retry_interval=30m)`；commit `928433fcd` 已 push fork。
- [ ] 首次真实 LLM/飞书失败后观察 30 分钟重试、部分群补发、永久错误 `manual_repair` 停止。

### v4.6：报告生成任务队列（2026-11-25 已发布 stable）

> 背景：`GenerateForAllUsers` 是「内存名单 + 串行 for 循环」。三种情况下尾部用户既没有报告、也没有失败记录，因此不会进入 v4.5 的生成重试队列：
> ① 30 分钟批预算耗尽；② 进程中途重启；③ 某个用户卡在 120s LLM 超时上拖住整队。
> 而且调度器在跑批**之前**就写 `last_run`，当天不会再触发，事后无法区分「没轮到」和「用量低被跳过」。

**设计（关键决策）**

- **复用 `report_retry_tasks`**，语义从「失败重试」扩展为「生成任务」，不新建表（省掉唯一索引/UPSERT/重试调度/前端徽标）。
- **`running` 不落库**：`status=pending` 且 `lease_until > now()` 即视为正在生成。原因：修改 `idx_report_retry_tasks_generation_active` 的谓词会让滚动/回滚期间**旧镜像**的 `ON CONFLICT ... WHERE status IN ('pending','manual_repair')` 找不到匹配索引而报错。加列是纯增量，零兼容风险。
- **状态**：`pending / done / skipped / failed / manual_repair`；`skipped` 独立于 `failed`（无用量/低于日报阈值/已有成功报告不算失败，不触发告警）。
- **重试**：瞬时错误 → `pending + next_retry_at=+30min`，`attempts+1`；**上限 6 次**后转 `failed`；永久错误（401/403/404、敏感内容、非法类型）→ `manual_repair`；预算取消不计次，1 分钟后重排。
- **`skipped` 不在报告列表展示**（只进任务台账），避免列表被低用量用户刷屏。

**实现**

- 迁移 `253_report_generation_tasks.sql`（纯增量）：`lease_owner/lease_until/outcome/suppress_push/last_attempt_at` 五列；CHECK 放宽到五种状态；新增 `idx_report_retry_tasks_lease`；**不动** generation_active 唯一索引谓词。
- 仓储 `report_retry_repo.go`：`InsertPending`（幂等上账）、`InsertCompleted`（已有成功报告补 done 台账，让「应生成 N 人」完整）、`ClaimGeneration`（`FOR UPDATE SKIP LOCKED` + 租约）、`FinishGeneration`（写结局、可选 `attempts+1`、释放租约）、`SummarizeByPeriod`（每用户最新一条）、`ListByPeriod`、`ResetByPeriod`（含被取代旧任务收尾，避免唯一索引冲突）。
  - **领取顺序带类型优先级**：`daily → weekly → monthly`。因为三种类型现在会在同一分钟内一起上账，若乱序执行，周报/月报会聚合到还缺当天子报告的数据（旧实现是「日报跑完才跑周报」的隐式顺序）。同类型内按 `next_retry_at, id`。
- 服务 `report_tasks.go`：`MaterializeReportTasks`（上账；定时批次跳过已完成用户，手动批次保持「整体重生成」语义）、`RunGenerationTasks`（每轮领 4 条并发执行，到预算就停）、`ProcessReportTask`（结局分类）、`ReportTaskSummaryFor`、`ResetReportTasks`。
- `GenerateReport` 拆出 `generateReport(..., manageRetry bool)`：任务路径不再写重试任务，由 claim/finish 统一计次，避免 attempts 双增。
- 调度器：`report_scheduler.go` 只调 `MaterializeReportTasks`（2 分钟预算），不再同步跑批；`report_retry_scheduler.go` 每轮先 `RunGenerationTasks`（8 分钟预算、并发 4）再处理推送重试，run 预算 12 分钟。
- 顺带修复：重试时 `suppressAutoPush` 之前硬编码 `false`，会把「周报/月报发布日被抑制的日报」推出去；现在任务表带 `suppress_push`。
- 接口：`GET /admin/reports/tasks/summary`、`POST /admin/reports/tasks/retry`；`generate-all` 返回值增加 `pending/skipped/failed/manual_repair/total`（旧字段保留）。
- 前端：报告页新增「生成进度」卡片（应生成/完成/生成中/待生成/失败/需人工/已跳过 + 未完成名单展开 + 补跑按钮），i18n zh/en。
- 权限：补跑按钮沿用 admin 路由组，**所有 admin 可用**（与 generate-all 一致）。

**验证（2026-11-25）**

- `go build ./...`、`go vet`（service/repository/handler）全绿；`go test ./internal/service ./internal/handler/... ./internal/repository ./migrations` 全绿。
- 新增用例 `report_tasks_test.go`：上账幂等/跳过已完成、成功→done、无用量→skipped、500→pending+30min、404→manual_repair、6 次→failed、ctx 取消不计次、预算为 0 时全部保持 pending 且无租约、只重置未完成任务。
- 迁移测试 `253_report_generation_tasks_test.go`：断言纯增量（无 DROP INDEX/ALTER COLUMN/DROP COLUMN）。
- 前端 `vue-tsc --noEmit` EXIT=0。
- dev 33336 已启动并执行迁移 253；五列/CHECK/索引核对通过；用真实 schema 的事务验证了 `InsertPending` 幂等、`ClaimGeneration` 领租约、汇总计数 `running`、`FinishGeneration`、`ResetByPeriod`、`ListByPeriod` 的 SQL 形态。
- 新旧路由：`/tasks/summary`、`/tasks/retry` 返回 401（未登录）而非 404，确认已注册。
- 生产旧镜像在迁移后继续正常运行，无 `report_retry` 相关报错（纯增量兼容）。
- **dev 真实端到端（2026-11-25 14:00）**：临时插入随机 `admin_api_key`（用完即删，未新建用户/未改密码）→ `POST /admin/reports/generate-all {daily, 2026-11-25}`：14 个活跃用户 **45 秒全部生成**，返回 `generated=14,pending=0,failed=0,total=14`；`/tasks/summary` 显示 `done=14`；台账 14 行 `done/generated`、无残留租约；报告 `pushed_at` 全为空（手动批次不推）。随后把 1 条任务改成 `manual_repair` 调 `/tasks/retry` → `reset=1`，状态回到 `pending/attempts=0`。验证后删除当日 14 份报告与任务、删除临时 key，库恢复测试前状态。
- **崩溃恢复 SQL 验证**：活租约任务领取返回 0 行；租约过期后同一任务被重新领取（`lease_owner` 从 `dead` 变为 `rescuer`）。
- **发布（2026-11-25 14:00）**：备份 `sub2api:stable-backup-20261125-1400` → `sub2api:dev` 打 stable → 重建 33333；镜像 `094dbcb4991f`、`/health` 200、`[ReportRetry] scheduler started`；新路由返回 401（已注册）；`report_retry_tasks` 为空。
- **今晚 20:00 观察点**：`[ReportScheduler] materialized daily ... candidates=N created=N`；20:30 后 `select status,count(*) from report_retry_tasks where period_start=<今日> group by 1` 应全为 `done`（低用量用户为 `skipped`）；管理端「生成进度」卡片数字与 `reports` 对账；若 `failed/manual_repair > 0` 点「补跑未完成」。

### 生产修复：提示词审计扫描器恢复（2026-11-25）

> 现象：生产 `prompt_audit.scan_chunk_failed` 持续报 `prompt_guard_unavailable`，事件全部降级为 `scan_failed:prompt_guard_unavailable`；扫描耗时常只有 50ms 左右，说明是上游快速拒绝，不是超时。

**根因（三层叠加）**

1. **base_url 缺 `/v1`**：`https://api.minimaxi.com` + `/chat/completions` → nginx 404。
2. **模型下线**：`MiniMax-M2` 返回 `The model "MiniMax-M2" is no longer available. (2069)`；`MiniMax-M2.7` 正常。
3. **编码套餐 key 限流**：修正端点/模型后，部分 chunk 成功，但随后大量 429。实测该 `sk-cp-...` key 顺序 20 次请求约 8 次 200、12 次 429；4 workers × 2000 字符 chunk 会迅速打满 RPM，任务重试耗尽后再次降级。

**修复**

- 2026-11-25 10:52：先把 base_url 改为 `https://api.minimaxi.com/v1`、模型改为 `MiniMax-M2.7`（config_version=2），确认端点/模型可用；仍受 RPM 限制。
- 2026-11-25 10:55：切换审计节点为 **DeepSeek**：
  - `base_url=https://api.deepseek.com/v1`
  - `model=deepseek-chat`
  - token 使用账号 id=8 的 key，AES-256-GCM 重新加密写入 `token_ciphertext`
  - `input_limit=16000`（减少 chunk 数）
  - `worker_count=2`
  - `timeout_ms=15000`
  - config_version=3
- 旧配置备份：`/tmp/prompt_audit_config.backup.json`（仅本机临时文件，关键字段已记录在上方）。

**验证**

- 新 `config_version=3` 任务：`scan_chunk_failed` **0 条**，`scan_chunk_completed` 持续增长；事件 `scanner_version=deepseek-chat`。
- 仍出现的 `scan_failed` 均来自旧 `config_version 1/2` 任务，属于修复前的重试/降级尾巴，会自然排空。
- DeepSeek 实测：单条 16k 字符 chunk + 1024 max_tokens 约 0.3s 返回，格式能正确输出 `Safety/Categories`；20 并发 4 worker 全部 200。

**后续建议**

- 当前临时复用账号 id=8 的 DeepSeek key，建议换独立审计 key/额度，避免和用户流量争配额。
- 增加扫描器请求速率限制/429 退避，避免高并发再次打满。
- `scan_chunk_failed` 日志补 `http_status`/`retryable` 字段，下次排查更快。
- 对 `scan_failed:*` 占比加告警（例如 10 分钟 >5%）。

### 后续迭代

- [ ] **节假日表年度维护**：每年 11 月国办公布次年放假安排后，在 `backend/internal/pkg/holiday/holiday.go` init() 补表并走 §4 发布流程（2027 年表未内置前自动按「仅避开周末」回退，日志会 warn 一次）
- [ ] 飞书推送优化（分群/自建应用/推送状态——推送状态已由 v3.5 落库，剩分群/自建应用/失败重试队列，见 §10 后续优化）
- [x] 2026-09-20 审查遗留中三项已于 v4.2 解决：`MaxPrompts` 死配置接线、64k 头尾拼接保留最新轮次、LLM 上下文总预算 cap
- [ ] 代码审查中优遗留（2026-09-20 审查结论，v4.2 后的剩余项）：批量生成异步化（generate-all 30min 同步阻塞）、每用户 N+1 查询、persist 改 UPSERT、cron 保存校验、脱敏值回写覆盖风险、`left(2000)` 截断丢未闭合 reminder、前端列表竞态/分页/admin-user 视图抽组件、prompt_audit_events 保留策略、推送失败重试队列

### 已完成（归档）

- [x] push 到 fork 完成（CGKBAI/sub2api；最新 7c3f80740）
- [x] 月报类型 + 模板化 LLM prompt 上线（2026-09-16）
- [x] 双通道报告素材 + 审计 session_id 入库（2026-09-16 晚，commit 40ab07449）
- [x] LLM 配置修复：base_url 补 /v1、max_prompts=60、truncate=800（SQL 直改 settings 已生效）
- [x] 飞书推送上线（2026-09-17，commit 7c3f80740，见 §10）

## 9. 环境速记

- `.env`：`/home/xxy/sub2api-deploy/.env`（含密钥，600 权限，**勿泄露/提交**）
- 本机工具：Node 24、Docker 29 + buildx（已装 `~/.docker/cli-plugins/docker-buildx`）、git；**无 Go**（编译走 golang 容器）
- 时区 Asia/Shanghai；deepseek 账号 id=1 的 key **已失效**；可用的上游：deepseek id=2、MiniMax id=5（zhipu 为 coding 域不兼容 /v1 拼接）
- 大量历史 failed job（~312 条，deepseek 时代+切换窗口）的 prompt 已随 Redis payload TTL 丢失，无法回补；此后消息全量留存

## 10. 飞书推送（2026-09-17 已上线生产）

> 已确认决策：①**群自定义机器人 Webhook**（v4.1 起支持多群，群组与成员在「报告群组」页维护；仍非自建应用，不支持文件消息）；②**interactive 卡片消息**：报告标题进卡片 header，正文 markdown；③**定时+手动生成成功后自动推**，另有前端"发送到飞书"按钮（本人 user 页 + admin 页任何人），v4.1 起按报告作者所属所有群投递；④日/周/月**三种类型独立推送开关**（全局，不按群配置）；⑤用户级"参与推送"开关 `report_push_enabled`（users 表 bool 列，**默认 false**，关闭后不参与自动推送；手动按钮推送不受该开关限制）。推送 best-effort：失败仅日志，不影响报告生成。

### 阶段 1：配置 + 飞书客户端 + 单测 ✅ 已完成

- [x] `ReportLLMConfig`（service/report_llm.go，settings 表 `report_config` 热加载）扩展：`feishu_enabled`、`feishu_webhook_url`、`feishu_secret`（可选加签）、`feishu_push_daily/weekly/monthly`；default/normalize 补齐
- [x] admin config 接口：`updateReportConfigRequest` 加指针字段；`redactReportConfig` 对 webhook_url/secret 脱敏（`********`，留空=保留旧值，仿 api_key）
- [x] 新文件 `service/report_feishu.go`（仿 reportLLMClient 惯例：固定超时 http.Client、WithContext、LimitReader、`ErrReportFeishuPushFailed`）：首行 `# ` 提取为 header.title、`## x`→`**x**`（卡片 md 不支持标题语法，其余列表/粗体原生兼容）、超 28000 字节 rune 安全截断；配 secret 时 HMAC-SHA256(`timestamp+"\n"+secret`) base64 加签；响应 `code!=0`（兼容 {code,msg}/{StatusCode,StatusMessage} 两格式）视为失败带回 msg；无 AI 摘要时正文退化为统计行
- [x] 单测：`report_feishu_test.go`（标题提取/粗体转换/退化/截断/签名/normalize 7 例全绿）

### 阶段 2：自动推送钩子 + 手动推送 API + 用户开关 ✅ 已完成

- [x] 钩子：GenerateReport `persist` 成功且 `status=done` 后 `maybeAutoPushFeishu`；条件 = feishu_enabled && 类型开关 && webhook 非空 && `IsReportPushEnabled(userID)`；独立 10s 超时 best-effort。定时/手动/admin 批量生成天然全覆盖，**scheduler 零改动**
- [x] 手动推送 API：`POST /api/v1/user/reports/:id/push`（PushUserReport 校验本人，越权按 not found）+ `POST /api/v1/admin/reports/:id/push`（PushReport）；只要求 webhook 已配置（不查类型开关/用户 toggle）；失败信息回传前端 toast；复用现有 repo GetByID
- [x] 迁移 `235_user_report_push_enabled.sql`：`ALTER TABLE users ADD COLUMN IF NOT EXISTS report_push_enabled BOOLEAN NOT NULL DEFAULT true;`（纯增量；33336 启动已验证列生效）
- [x] ent schema user.go 加 `report_push_enabled` Default(true) → 容器 `go generate ./ent && ./cmd/server`（注：镜像里无 make/git，直接跑两条 go generate；wire 生成物已更新）；User struct / UpdateProfileRequest `*bool`（复用 PUT /api/v1/user patch 语义）/ UserUpdateFields / user_repo field-mask / api_key_repo userEntityToService / dto User+mapper / user_handler 全链路打通

### 阶段 3：前端 ✅ 已完成

- [x] admin ReportsView 设置对话框"飞书推送"分区（enabled、webhook/secret 密码框留空不改、三类型 checkbox）；api/admin/reports.ts config 类型 + pushToFeishu
- [x] user/admin 报告卡片"发送到飞书"按钮（loading + toast）；api/reports.ts pushMyReportToFeishu
- [x] user ReportsView 页头"参与飞书自动推送"开关（getProfile 读 + updateProfile 写 report_push_enabled，失败回滚）
- [x] i18n zh/en admin/reports.ts（actions.push/autoPush + config.feishu* ）
- [x] types/index.ts User + api/user.ts updateProfile 加 report_push_enabled；vue-tsc 通过

### 阶段 4：验证发布 ✅ 已完成

- [x] go build/vet + 相关单测全绿（repo/payment 存量 FAIL 干净树复现确认与本次无关）→ buildx dev 镜像 → 33336 冒烟：HTTP 200、迁移 235 生效、无 panic
- [x] webhook 已配置（settings 表 jsonb 合并写入，ConfigManager 热加载）+ 测试卡片发送成功（机器人返回 StatusCode+code 双格式，验证了响应兼容解析）
- [x] 网页验证通过（用户确认：生成收卡片/按钮/自动推链路 OK）
- [x] 发 stable（33333 healthy，HTTP 200）→ commit 7c3f80740 + push fork（CGKBAI/sub2api）

### 后续优化（发送方式迭代方向，待做）

- [x] 按用户分群（多 webhook）：v4.1 已实现（群组 + 成员多对多 + 每群投递记录）
- [ ] 自建应用通道（上传 .md 文件消息、发个人 open_id）
- [ ] 单群失败重试队列/按钮（当前每个群独立记录失败原因，可手动整份重推）
- [x] 推送状态记录（`reports.pushed_at`/`last_push_error` + v4.1 `report_feishu_deliveries`）
- [ ] 卡片模板美化（跳转回报告页按钮）
