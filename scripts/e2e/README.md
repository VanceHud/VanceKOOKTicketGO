# 端到端 UI 验收脚本（可选）

这两个脚本用于在**不接入真实 KOOK** 的前提下验收界面与接口的联通性：
它们用 Playwright 驱动真实浏览器，覆盖登录、仪表盘、工单列表/详情、备注、设置、主题与语言切换、
命令面板、账号页、登录码登录与一次性校验等关键路径，并在失败时自动截图。

## 使用前提

脚本依赖 Playwright 与 Chrome，但**不会**写入项目依赖（避免给常规构建增加体积）：

```bash
# 任选其一：使用系统已安装的 Chrome（脚本默认 channel: chrome）
npm install -g playwright        # 或 npm install playwright（在任意目录）
```

## 运行

```bash
# 1. 启动一个带演示数据的实例（另开一个终端，或在容器里运行）
KOOK_DRYRUN=1 ADMIN_PASSWORD='SmokeTest@2026kt' PORT=8080 go run ./cmd/server

# 2. 运行流程验收
ADMIN_PASSWORD='SmokeTest@2026kt' node scripts/e2e/01-core-flows.mjs

# 3. 运行“登录码 + 机器人状态”验收
#    先往数据库写入一个一次性登录码（等价于在 KOOK 私聊机器人发送 /login 得到的码）：
python3 - <<'PY'
import sqlite3, hashlib, datetime
con = sqlite3.connect("./data/ticket.db")
now = datetime.datetime.now(datetime.timezone.utc)
fmt = lambda d: d.strftime('%Y-%m-%d %H:%M:%S+00:00')
code = "XYZ789"
con.execute("DELETE FROM auth_codes")
con.execute(
    "INSERT INTO auth_codes (code_hash, purpose, kook_user_id, kook_user_name, role_hint, expires_at, created_at)"
    " VALUES (?,?,?,?,?,?,?)",
    (hashlib.sha256(code.encode()).hexdigest(), "login", "9500", "验收客服", "staff",
     fmt(now + datetime.timedelta(minutes=10)), fmt(now)),
)
con.commit()
print("登录码已写入：", code)
PY

LOGIN_CODE='XYZ789' ADMIN_PASSWORD='SmokeTest@2026kt' node scripts/e2e/02-kook-and-codes.mjs
```

可用环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `BASE` | `http://127.0.0.1:8080` | 被测服务地址 |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | `admin` / `SmokeTest@2026kt` | 管理员凭据 |
| `LOGIN_CODE` | `XYZ789` | 第二步使用的登录码（需与服务端库中一致） |
| `OUT_DIR` | `/tmp/kook-ticket-e2e` | 截图输出目录 |

另外还有 `03-manage-and-stats.mjs`：验收统计看板、面板管理、表情规则增删改与北京时间标注，
不需要登录码，直接用管理员密码运行即可：

```bash
ADMIN_PASSWORD='SmokeTest@2026kt' node scripts/e2e/03-manage-and-stats.mjs
```

脚本退出码非 0 表示存在断言失败、控制台错误或失败请求。
每次运行 `02` 脚本都要**重新写入一个登录码**（一次性码第一次登录后即作废，
重复运行同一个码会看到“登录码无效或已过期”，这是预期行为）。
注意：`02` 脚本会**故意**触发一次“机器人重连失败”（DryRun 模式未配置 Token），
因此浏览器控制台会出现一条 502 记录，这是预期行为。
