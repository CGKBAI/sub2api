# sub2api 日报/周报/月报功能 — 项目状态（供新 session 接续）

> 本文档是完整项目上下文。最后更新：2026-09-16（新增月报类型 + LLM 总结 prompt 融入 work-report 标准模板，已上线生产）。

## 1. 功能与当前状态总览

在自部署 sub2api（Wei-Shaw/sub2api，AI API 网关）上新增**日报/周报/月报**能力：

1. ✅ **Prompt 存储**（核心，已完成）：异步审计落库 `prompt_audit_events`，**扫描失败也必存**（降级落库）
2. ✅ **fork 改造**：Web 界面（admin 看所有人 / user 看自己）、定时生成、手动生成、LLM 设置页
3. ✅ **已上线生产**（33333，`sub2api:stable`）
4. ✅ **月报类型**（2026-09-16）：迁移 233 放宽 CHECK；月报固定覆盖 ref 的**上一个自然月**（每月 1 日 20:20 生成上月，手动生成语义一致）；聚合优先级：当月周报 → 当月日报 → prompt 片段
5. ✅ **LLM 总结 prompt 重写 ×2**（2026-09-16）：最终格式对齐团队模板——**标题由后端拼**（`reportTitle()`，姓名取 users.username，如 `# 工作日报（2026-09-15）- 谢翔宇`），LLM 只输出两个小节（`## 一、今日/本周/本月核心工作` + `## 二、明日/下周/下月工作计划`，平铺编号条目，无分类/优先级标注）；素材规则保留噪音过滤/合并同类/量化/脱敏；`ReportRepository.GetUsername()` 新增
6. ✅ **报告素材双通道**（2026-09-16 晚）：①`FetchUserTurns` 逐请求头部提取用户真实输入（剥 <system-reminder> 前缀+噪音过滤+去重）——普通聊天/Claude Code 客户端有效；②`FetchPromptSnapshots`+对话区窗口采样（锚点 `</available_skills>` 后，全天 4 快照×8 窗口×700 字）——opencode 等智能体客户端用户输入埋在历史深处、且 reminder 字符串会出现在系统提示词讲解和文件内容里导致正则剥离不可靠，只能靠窗口采样。system prompt 含 ❌/✅ 反例（禁止'用了什么工具/模式/多少请求'类条目）。验证：谢翔宇 9/16 预览输出已为真实工作条目。**已知限制**：①单 session 超 64k 时审计截断丢最新几轮（仅保头部，opencode 客户端压缩可部分缓解）；②快照按全天序号均匀选取、未按 session 分组——多 session 用户可能漏 session，待办第 1 项解决
7. ✅ **审计表 session 维度**（2026-09-16 晚）：迁移 234 给 prompt_audit_jobs/events 加 session_id（取自请求头 `ExtractClientSessionID` 单一入口），部分索引 (user_id, session_id, created_at)；Request→job→event 全链路穿透。注意：session_id 与 usage_logs 同源（客户端上报），历史数据为空
6. ⏸ **飞书推送**：暂缓

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
- 项目状态文档同步提交在 `docs/DEV-STATUS.md`（每次重要变更后更新并 commit）
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

## 4. 开发循环（新 session 按 此操作）

```bash
# 1. 改代码：/home/xxy/fs/sub2api
# 2. 构建开发镜像（有缓存约 1-2 分钟）
docker buildx build --load -t sub2api:dev /home/xxy/fs/sub2api
# 3. 刷新开发实例，浏览器验证 http://127.0.0.1:33336
cd /home/xxy/sub2api-deploy && docker compose -f docker-compose.dev.yml up -d --force-recreate
# 4. 验证满意 → 发布生产（约 20 秒中断）
docker tag sub2api:dev sub2api:stable && cd /home/xxy/sub2api-deploy && docker compose up -d sub2api
# 回退：改主 compose image 回 weishaw/sub2api:latest up -d（注意：无审计落库修复，会丢消息）
```

**纪律**：
- 改 ent schema / wire 后先 `docker run --rm -v /home/xxy/fs/sub2api/backend:/app -w /app -e GOFLAGS=-buildvcs=false golang:1.27.0-alpine sh -c "apk add --no-cache git >/dev/null 2>&1 && go get github.com/google/wire/cmd/wire@v0.7.0 >/dev/null 2>&1 && make generate"`，生成物提交仓库
- 编译验证**不要用管道吃退出码**：`go build ./... > /log 2>&1; echo $?`
- **新迁移必须纯增量**（只建新表/加可空列）：dev 启动会在共享库跑迁移，stable 共存
- 审计 worker 双实例都消费队列（都有"必存"修复，无丢失风险）

## 5. Prompt 存储事实（已核实）

- 表：`prompt_audit_events`（主）+ `prompt_audit_jobs`（队列），无自动清理，永久保留
- **格式**：`full_prompt` = 拍平纯文本（非 JSON）= [最后一条用户消息原文] + \n\n + [其余上下文（system/历史）]，段间无角色标记；截断 65536 runes（超长加 `…`）
- `redacted_preview` = 前 28 字符脱敏预览；`prompt_hash` = SHA256
- 元数据：user_id/user_email_snapshot/api_key_name_snapshot/model/provider/endpoint/created_at 等，`(user_id, created_at DESC)` 索引现成
- 扫描失败的事件：decision=pass + `scanner_version="scan_failed:<code>"`（降级标记，可 SQL 过滤）
- 查询示例：`SELECT created_at, model, full_prompt FROM prompt_audit_events WHERE user_id=9 AND created_at >= '2026-09-15' ORDER BY created_at;`

## 6. 审计与报告配置（存 settings 表，两实例共享生效）

| settings key | 当前值 |
|---|---|
| `risk_control_enabled` | true |
| `prompt_audit_config` | 异步审计（async，不阻断），审计节点：**MiniMax** `https://api.minimaxi.com` + `MiniMax-M2`（账号 id=5 的 key，AES-256-GCM 加密存 token_ciphertext，密钥=.env 的 TOTP_ENCRYPTION_KEY），input_limit=2000，timeout=15000ms |
| `report_config` | enabled=true，LLM=同 MiniMax 端点，max_prompts=30，单条截断 500 字符，日报 cron `0 20 * * *`，周报 `10 20 * * 5`，月报 `20 20 1 * *`（每月 1 日生成上月；存量 JSON 缺 monthly_schedule 时读取自动补默认） |

配置修改：直接 UPDATE settings 表（ConfigManager 每 5 秒 TTL reload，无需重启）；endpoint token 必须先用 TOTP_ENCRYPTION_KEY 做 AES-256-GCM 加密（base64(nonce+ct+tag)，node crypto 可做）。

## 7. fork 相对官方 v0.1.184 的改动清单（rebase 时注意）

**新增文件**（reports 功能全套）：
- `backend/internal/domain/report.go`、`ent/schema/report.go`、`migrations/232_reports.sql`（reports 表，唯一索引 user_id+type+period_start）、`migrations/233_reports_monthly.sql`（CHECK 放宽加 'monthly'，纯增量、旧版本兼容）
- `backend/internal/service/report.go|report_service.go|report_llm.go|report_scheduler.go`
- `backend/internal/repository/report_repo.go`（usage_logs 聚合 + prompt 拉取均原生 SQL）
- `backend/internal/handler/dto/report.go`、`handler/admin/report_handler.go`、`handler/user_report_handler.go`
- 前端：`api/admin/reports.ts|api/reports.ts`（共享 `ReportType` 类型）、`views/admin|user/ReportsView.vue`（三 tab：日/周/月 + 月报 cron 配置）、i18n `zh|en/admin/reports.ts`

**修改官方文件（挂载点）**：`config.go`（ReportConfig）、`handler.go|handler/wire.go`、`service/wire.go`、`repository/wire.go`、`routes/admin.go|user.go`、`cmd/server/wire.go`（cleanup）、`domain_constants.go`（SettingKeyReportConfig）、router/index.ts、AppSidebar.vue（ReportIcon）、i18n common.ts nav + admin/index.ts ×2、go.mod/go.sum（wire cmd indirect）

**对官方审计代码的 3 处关键修改**（`securityaudit/`）：
1. `prompt_qwen3guard.go`：scan 请求加 system prompt（让任意 OpenAI 兼容模型输出 `Safety:/Categories:` 格式）+ max_tokens 64→1024（思考型模型需要预算）
2. `prompt_worker.go`：**扫描失败也落库**（fallback result decision=pass + scanner_version 标记，Complete 强制写 event，job 记 done）——消息必存的核心
3. `report_llm.go`：输出剥离 `<think>...</think>`

## 8. 待办

- [ ] **【下一步·已确认方案】快照选取改为 session 感知**：`FetchPromptSnapshots`（backend/internal/repository/report_repo.go）重写——
  ① `session_id <> ''` 的行 `GROUP BY session_id` 各取 `created_at` 最大一条（= 该 session 最全快照；session 数上限 8，超过取最近 8 个）；
  ② `session_id = ''` 的行（历史数据/无会话头客户端）保留现有 `rn % (total/count)` 均匀分布兜底；
  ③ 两路合并按时间正序返回。service 层窗口预算按 session 均分（总 ~32 窗、每 session ≤8 窗 ×700 字，report_service.go 常量 `reportSnapshotCount`/`conversationWindowsPerSnap` 相应调整）。
  背景与实测：多 session 是常态（2026-09-16 usage_logs：user 5/6/8 各 3 个 session、user 2 两个）；当前②通道按全天请求序号 25%/50%/75%/100% 取快照，多 session 时可能漏整个 session 的对话区。
  注意：9/16 白天旧数据 session_id 为空（迁移 234 之前），重生成走兜底路径效果与现状相同；session 分组对当晚 20:00 后的新数据生效。改完：单测 + go build + buildx 镜像 → 33336 冒烟 → 发 stable → commit + push fork（流程见 §4）
- [ ] 用户验证：33333 网页登录 → "日报周报月报"页 → 日期选 2026-09-16 → "为所有活跃用户生成"，确认日报为真实工作条目（双通道已用真实数据预览验证：谢翔宇输出"调研 work-report 模板库/梳理 report 代码/规划整合方案"等真实条目）
- [ ] 验证当晚 20:00 定时日报自动生成（新 prompt + 双通道素材 + 修好的 /v1 base_url）
- [ ] 验证周五 20:10 首次周报 + 10 月 1 日 20:20 首次月报（月报聚合当月周报）
- [x] push 到 fork 完成（CGKBAI/sub2api，2026-09-15；最新 f1c1e5fb2）
- [x] 月报类型 + 模板化 LLM prompt 上线（2026-09-16）
- [x] 双通道报告素材 + 审计 session_id 入库（2026-09-16 晚，commit 40ab07449）
- [x] LLM 配置修复：base_url 补 /v1、max_prompts=60、truncate=800（SQL 直改 settings 已生效）
- [ ] （后续迭代）飞书推送

## 9. 环境速记

- `.env`：`/home/xxy/sub2api-deploy/.env`（含密钥，600 权限，**勿泄露/提交**）
- 本机工具：Node 24、Docker 29 + buildx（已装 `~/.docker/cli-plugins/docker-buildx`）、git；**无 Go**（编译走 golang 容器）
- 时区 Asia/Shanghai；deepseek 账号 id=1 的 key **已失效**；可用的上游：deepseek id=2、MiniMax id=5（zhipu 为 coding 域不兼容 /v1 拼接）
- 大量历史 failed job（~312 条，deepseek 时代+切换窗口）的 prompt 已随 Redis payload TTL 丢失，无法回补；此后消息全量留存
