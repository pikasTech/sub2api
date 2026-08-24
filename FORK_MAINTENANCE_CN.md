# Sub2API Fork 维护约定

本仓库用于在保留官方 Sub2API 主线同步能力的前提下，维护可选的 Codex OAuth 额度透支功能。

## 分支与远端

- `upstream`：官方 Sub2API 仓库，只用于同步官方历史和主线。
- `origin`：`pikasTech/sub2api` fork，用于保存本仓库分支。
- `main`：官方主线镜像，不直接提交定制功能。
- `feature/codex-quota-overdraft`：透支功能开发分支。
- `release/*-overdraft.*`：经过验证的定制发布分支。

## 当前开关

- 配置键：`gateway.codex_quota_overdraft_enabled`。
- 环境变量：`GATEWAY_CODEX_QUOTA_OVERDRAFT_ENABLED`。
- 默认值：`false`。
- 当前阶段只建立开关，不接入透支请求注入、账号暂停或探测逻辑。

## 同步原则

- 官方主线更新先进入 `upstream/main`，再在 feature 分支上 rebase。
- 透支功能按独立提交维护，不使用长期未提交 diff 作为唯一备份。
- 同步后必须检查功能开关仍默认为关闭，并确认开关关闭时正常请求行为不变。
- 生产构建只从经过验证的 `release/*-overdraft.*` 分支生成，不直接从开发分支发布。
