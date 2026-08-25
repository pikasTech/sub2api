---
name: sub2api-admin
description: Manage Sub2API admin APIs for accounts, redeem codes, groups, proxies, error passthrough rules, TLS fingerprint profiles, imports, exports, batch updates, and raw administrator API calls. Use when the user mentions Sub2API, admin API keys, account management, redeem code management, recharge codes, invitation codes, bulk account import/export, keeping or deleting accounts, refreshing accounts, clearing errors, CRS sync, or managing Sub2API backend settings through the admin API.
---

# Sub2API Admin

Use the bundled CLI instead of ad hoc `curl`. Run examples from this skill directory.

```bash
export SUB2API_BASE_URL='https://your-sub2api-host'
export SUB2API_ADMIN_API_KEY='<admin api key>'
# Or, when the deployment uses admin JWT login instead of an admin API key:
# export SUB2API_JWT='<admin access_token>'
node scripts/sub2api-admin.js accounts list
```

For all commands and payload examples, read [references/admin-cli.md](references/admin-cli.md).

## Workflow

1. Reuse `SUB2API_BASE_URL` and either `SUB2API_ADMIN_API_KEY` or `SUB2API_JWT` from the environment.
2. Run read-only commands first: `accounts list`, `accounts get <id>`, `groups all`, or `proxies all`.
3. Before destructive or bulk writes, print the target account names and IDs.
4. Execute the write command only after the target set is clear.
5. Run a follow-up read command to verify the result.

## Common Commands

```bash
node scripts/sub2api-admin.js accounts list --page-size 20
node scripts/sub2api-admin.js accounts get 40
node scripts/sub2api-admin.js accounts usage 40
node scripts/sub2api-admin.js accounts set-schedulable 40 true
node scripts/sub2api-admin.js accounts bulk-update --ids 40,39 --json '{"concurrency":10}'
node scripts/sub2api-admin.js redeem-codes list --page-size 20
node scripts/sub2api-admin.js redeem-codes generate --json '{"count":1,"type":"balance","value":10}' --idempotency-key redeem-$(date +%s)
node scripts/sub2api-admin.js redeem-codes create-and-redeem --json '{"code":"order_123","type":"balance","value":10,"user_id":123}' --idempotency-key order-123
node scripts/sub2api-admin.js error-rules list
node scripts/sub2api-admin.js tls-profiles list
```

## 容器化前端验证

CLI 可在 Node 20 Docker 容器中挂载当前 fork，固定 pnpm 9，使用独立容器卷安装依赖，避免受 host 的 Corepack/pnpm 版本影响：

```bash
node scripts/sub2api-admin.js dev test-frontend start --repo /root/sub2api --suite typecheck
node scripts/sub2api-admin.js dev test-frontend status <job-id> --repo /root/sub2api
node scripts/sub2api-admin.js dev test-frontend logs <job-id> --repo /root/sub2api --tail 200
node scripts/sub2api-admin.js dev test-frontend wait <job-id> --repo /root/sub2api --timeout 120s
node scripts/sub2api-admin.js dev test-frontend clean <job-id> --repo /root/sub2api
```

- `--suite` 支持 `typecheck`、`build`、`lint`、`critical` 和 `all`，默认是 `typecheck`。
- `start` 为异步启动，立即返回 job ID；通过 `status`、`logs` 和 `wait` 查询结果。
- 默认使用 `node:20-bookworm` 与 pnpm `9.15.5`；可用 `--image` 和 `--pnpm-version` 覆盖。
- 源码目录挂载到容器 `/workspace`，依赖写入独立 Docker volume，不写入 host 的 `node_modules`。
- CLI 状态保存在 fork 的 `.state/sub2api-admin/container-tests/`，该目录不进入 Git。

## Safety Notes

- Authentication uses `x-api-key` from `SUB2API_ADMIN_API_KEY` first, then falls back to `Authorization: Bearer <jwt>` from `SUB2API_JWT`.
- If the API returns `INVALID_ADMIN_KEY`, ask the user to regenerate the admin API key. If using JWT, log in as an admin user and copy the `access_token` from `POST /api/v1/auth/login`.
- `accounts export` includes credentials and tokens. Prefer `--file` and avoid printing exports in chat.
- Redeem code create/redeem commands should use `--idempotency-key` for payment or recharge workflows.
- For uncertain or newly added backend APIs, use `api <METHOD> <admin-path>` after a read-only check.
