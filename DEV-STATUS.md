# sub2api 日报/周报/月报功能 — 项目状态（供新 session 接续）

> 本文档是完整项目上下文。最后更新：2026-09-18（**v3.3 已上线生产 stable 33333**：批量生成"为所有活跃用户"修复 Network error——前端超时覆盖 + 后端批量脱离请求 context；v3.2 报告页布局修复，见 §8）。

## 1. 功能与当前状态总览

在自部署 sub2api（Wei-Shaw/sub2api，AI API 网关）上新增**日报/周报/月报**能力：

1. ✅ **Prompt 存储**（核心，已完成）：异步审计落库 `prompt_audit_events`，**扫描失败也必存**（降级落库）
2. ✅ **fork 改造**：Web 界面（admin 看所有人 / user 看自己）、定时生成、手动生成、LLM 设置页
3. ✅ **已上线生产**（33333，`sub2api:stable`）
4. ✅ **月报类型**（2026-09-16）：迁移 233 放宽 CHECK；月报固定覆盖 ref 的**上一个自然月**（每月 1 日 20:20 生成上月，手动生成语义一致）——v3 将反转为 ref 所在自然月，见 §8；聚合优先级：当月周报 → 当月日报 → prompt 片段
5. ✅ **LLM 总结 prompt 重写 ×2**（2026-09-16）：最终格式对齐团队模板——**标题由后端拼**（`reportTitle()`，姓名取 users.username，如 `# 工作日报（2026-09-15）- 谢翔宇`），LLM 只输出两个小节（`## 一、今日/本周/本月核心工作` + `## 二、明日/下周/下月工作计划`，平铺编号条目，无分类/优先级标注）；素材规则保留噪音过滤/合并同类/量化/脱敏；`ReportRepository.GetUsername()` 新增
6. ✅ **报告素材双通道**（2026-09-16 晚）：①`FetchUserTurns` 逐请求头部提取用户真实输入（剥 <system-reminder> 前缀+噪音过滤+去重）——普通聊天/Claude Code 客户端有效；②`FetchPromptSnapshots`+对话区窗口采样（锚点 `</available_skills>` 后，每 session 最全快照×均分窗口×700 字）——opencode 等智能体客户端用户输入埋在历史深处、且 reminder 字符串会出现在系统提示词讲解和文件内容里导致正则剥离不可靠，只能靠窗口采样。system prompt 含 ❌/✅ 反例（禁止'用了什么工具/模式/多少请求'类条目）。验证：谢翔宇 9/16 预览输出已为真实工作条目。**已知限制**：单 session 超 64k 时审计截断丢最新几轮（仅保头部，opencode 客户端压缩可部分缓解）；快照均匀选取未按 session 分组的限制已由 v2 解决（见第 8 项）
7. ✅ **审计表 session 维度**（2026-09-16 晚）：迁移 234 给 prompt_audit_jobs/events 加 session_id（取自请求头 `ExtractClientSessionID` 单一入口），部分索引 (user_id, session_id, created_at)；Request→job→event 全链路穿透。注意：session_id 与 usage_logs 同源（客户端上报），历史数据为空
8. ✅ **报告链路 v2+v3**（2026-09-16/17，v3 已上线 stable）：①日报素材按 session 分组总结（`FetchPromptSnapshots`，session_id 自 9/16 17:54 起入库）——上线保留；②周报周期=**正常周一~周日**（v2 的周六周期已回滚，`StartOfWeekSaturday` 已删除），周五 20:10 定时生成覆盖周一~周五、周末工作后续手动重生成补入；③月报=**ref 所在自然月**（前端选 8 月出 8 月；调度器 1 日传上月 1 日生成上月）；④前端周报按周下拉选（近 12 周）、月报按月选（admin/user 双视图）；⑤**手动生成永不自动推飞书**（v3.1，`ReportTrigger` manual/scheduled），定时生成按类型开关+用户参与开关自动推；⑥月报聚合与当月有交集的周报（pageSize 8）
9. ✅ **飞书推送**（2026-09-17 已上线生产）：群自定义机器人 Webhook + interactive 卡片；生成成功自动推（类型开关+用户开关）+ 手动按钮（本人/admin）；commit 7c3f80740，见 §10

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
- **前端容器操作必须钉 pnpm@9**（`corepack prepare pnpm@9 --activate`，与 Dockerfile 一致）：corepack 默认的 pnpm v10 会重写 pnpm-lock.yaml、生成 pnpm-workspace.yaml、禁用 build scripts，导致镜像构建（--frozen-lockfile）失败
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

### 后续迭代

- [ ] 飞书推送优化（分群/自建应用/推送状态，见 §10 后续优化）

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

> 已确认决策：①**群自定义机器人 Webhook**（单群，非自建应用，不支持文件消息）；②**interactive 卡片消息**：报告标题进卡片 header，正文 markdown；③**定时+手动生成成功后自动推**，另有前端"发送到飞书"按钮（本人 user 页 + admin 页任何人）；④日/周/月**三种类型独立推送开关**；⑤用户级"参与推送"开关 `report_push_enabled`（users 表 bool 列，**默认 true**，关闭后不参与自动推送；手动按钮推送不受该开关限制）。推送 best-effort：失败仅日志，不影响报告生成。

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

- [ ] 按类型/按用户分群（多 webhook）、自建应用通道（上传 .md 文件消息、发个人 open_id）
- [ ] 推送状态记录与失败重试（reports 表加 pushed_at）、卡片模板美化（跳转回报告页按钮）
