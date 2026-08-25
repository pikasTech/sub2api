# Sub2API Fork 维护约定

本仓库用于在保留官方 Sub2API 主线同步能力的前提下，维护可选的 Codex OAuth 额度透支功能。

## 分支与远端

- `upstream`：官方 Sub2API 仓库，只用于同步官方历史和主线。
- `origin`：`pikasTech/sub2api` fork，用于保存本仓库分支。
- `main`：官方主线镜像，不直接提交定制功能。
- `feature/codex-quota-overdraft`：透支功能开发分支。
- `release/*-overdraft.*`：经过验证的定制发布分支。

## 当前开关与实际能力

- 管理入口：Sub2API 管理 Web 的 Gateway 设置页。
- API：`GET/PUT /api/v1/admin/settings/codex-quota-overdraft`。
- 存储：Sub2API `settings` 表中的 `codex_quota_overdraft_settings`。
- 默认值：`enabled=false`。
- YAML/环境变量不是该开关的 authority；修改后由运行时读取数据库设置，无需滚动。
- 开关开启后，Sub2API 会对符合条件的 Codex Responses 请求注入一组已完成的
  `custom_tool_call`/`custom_tool_call_output`，以触发上游额度透支计量路径。
- 注入覆盖普通 HTTP、WSv2 透传、WS HTTP Bridge 和 WS ctx-pool 的
  `response.create`；只处理 OpenAI OAuth-like 账号，API-key 账号不会注入。
- 请求必须包含顶层 `input` 数组且最后一项是用户 `message`；不满足条件时保持原请求，
  注入失败也保持原请求，单请求体上限为 `32 MiB`。
- 参考仓库中独立的账号管理层用量基线、透支价格和自动停用探测没有移植到 Sub2API；
  当前提交只改变上游请求体，不改变既有账号调度和配额状态机。

## Fork 验证 CLI

- 前端验证统一通过 `skills/sub2api-admin/scripts/sub2api-admin.js dev test-frontend` 执行。
- CLI 在 Node 20 Docker 容器中挂载 fork 源码，并固定使用 pnpm 9.15.5。
- `node_modules` 使用独立 Docker volume，避免 host 的 Corepack/pnpm 版本和依赖目录污染验证结果。
- 任务采用异步 job：通过 `start` 启动，使用 `status`、`logs`、`wait` 查询，完成后用 `clean` 回收容器和 volume。

## 同步原则

- 官方主线更新先进入 `upstream/main`，再在 feature 分支上 rebase。
- 透支功能按独立提交维护，不使用长期未提交 diff 作为唯一备份。
- 同步后必须检查功能开关仍默认为关闭，并确认开关关闭时正常请求行为不变。
- 生产构建只从经过验证的 `release/*-overdraft.*` 分支生成，不直接从开发分支发布。
