# sub2api 日报/周报功能 — 项目状态（供新 session 接续）

> 本文档是完整项目上下文。最后更新：2026-09-15（M1-M4 已完成，功能已上线，进入开发迭代期）。

## 1. 功能与当前状态总览

在自部署 sub2api（Wei-Shaw/sub2api，AI API 网关）上新增**日报/周报**能力：

1. ✅ **Prompt 存储**（核心，已完成）：异步审计落库 `prompt_audit_events`，**扫描失败也必存**（降级落库）
2. ✅ **fork 改造**：Web 界面（admin 看所有人 / user 看自己）、定时生成、手动生成、LLM 设置页
3. ✅ **已上线生产**（33333，`sub2api:stable`）
4. ⏳ **AI 总结质量优化**：用户明确"到时写一个 skill 来处理"，本期暂缓（当前用 MiniMax 直接总结，报告会含工具注入的 prompt 噪音）
5. ⏸ **飞书推送**：暂缓

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
| `report_config` | enabled=true，LLM=同 MiniMax 端点，max_prompts=30，单条截断 500 字符，日报 cron `0 20 * * *`，周报 `10 20 * * 5` |

配置修改：直接 UPDATE settings 表（ConfigManager 每 5 秒 TTL reload，无需重启）；endpoint token 必须先用 TOTP_ENCRYPTION_KEY 做 AES-256-GCM 加密（base64(nonce+ct+tag)，node crypto 可做）。

## 7. fork 相对官方 v0.1.184 的改动清单（rebase 时注意）

**新增文件**（reports 功能全套）：
- `backend/internal/domain/report.go`、`ent/schema/report.go`、`migrations/232_reports.sql`（reports 表，唯一索引 user_id+type+period_start）
- `backend/internal/service/report.go|report_service.go|report_llm.go|report_scheduler.go`
- `backend/internal/repository/report_repo.go`（usage_logs 聚合 + prompt 拉取均原生 SQL）
- `backend/internal/handler/dto/report.go`、`handler/admin/report_handler.go`、`handler/user_report_handler.go`
- 前端：`api/admin/reports.ts|api/reports.ts`、`views/admin|user/ReportsView.vue`、i18n `zh|en/admin/reports.ts`

**修改官方文件（挂载点）**：`config.go`（ReportConfig）、`handler.go|handler/wire.go`、`service/wire.go`、`repository/wire.go`、`routes/admin.go|user.go`、`cmd/server/wire.go`（cleanup）、`domain_constants.go`（SettingKeyReportConfig）、router/index.ts、AppSidebar.vue（ReportIcon）、i18n common.ts nav + admin/index.ts ×2、go.mod/go.sum（wire cmd indirect）

**对官方审计代码的 3 处关键修改**（`securityaudit/`）：
1. `prompt_qwen3guard.go`：scan 请求加 system prompt（让任意 OpenAI 兼容模型输出 `Safety:/Categories:` 格式）+ max_tokens 64→1024（思考型模型需要预算）
2. `prompt_worker.go`：**扫描失败也落库**（fallback result decision=pass + scanner_version 标记，Complete 强制写 event，job 记 done）——消息必存的核心
3. `report_llm.go`：输出剥离 `<think>...</think>`

## 8. 待办

- [ ] 用户验证：33333 网页登录 → "日报周报"页 → 手动生成（数据已具备，纯统计+MiniMax 总结）
- [ ] 验证今晚 20:00 定时日报自动生成（reports 表应出现当日记录）
- [x] push 到 fork 完成（CGKBAI/sub2api，2026-09-15）
- [ ] （后续迭代）AI 总结 skill：区分"用户真实输入"（优先级段）vs 工具注入 prompt（大段噪音），提升日报质量
- [ ] （后续迭代）飞书推送

## 9. 环境速记

- `.env`：`/home/xxy/sub2api-deploy/.env`（含密钥，600 权限，**勿泄露/提交**）
- 本机工具：Node 24、Docker 29 + buildx（已装 `~/.docker/cli-plugins/docker-buildx`）、git；**无 Go**（编译走 golang 容器）
- 时区 Asia/Shanghai；deepseek 账号 id=1 的 key **已失效**；可用的上游：deepseek id=2、MiniMax id=5（zhipu 为 coding 域不兼容 /v1 拼接）
- 大量历史 failed job（~312 条，deepseek 时代+切换窗口）的 prompt 已随 Redis payload TTL 丢失，无法回补；此后消息全量留存
