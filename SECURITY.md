# 安全政策

## 支持的版本

安全修复只针对最新发布版本与 `main` 分支。请先升级到最新版本再报告问题。

| 版本 | 是否接收安全修复 |
|---|---|
| 最新 release / `main` | ✅ |
| 更早的版本 | ❌ |

## 报告漏洞

**请不要用公开 issue 报告安全漏洞。**

优先使用 GitHub 的私密渠道：

👉 [Security → Report a vulnerability](https://github.com/VanceHud/VanceKOOKTicketGO/security/advisories/new)

（如果上面的链接不可用，也可以开一个不含任何细节的 issue，只写「需要私下沟通安全问题」，维护者会主动联系你。）

报告里请尽量包含：

- 受影响的版本或 commit
- 复现步骤或最小 PoC（能贴出 HTTP 请求/响应最好）
- 影响范围与你判断的严重程度
- 是否已在别处公开、你希望如何署名

## 响应时间

这是个人维护的项目，不承诺 SLA，但会尽力：

| 阶段 | 目标 |
|---|---|
| 确认收到 | 7 天内 |
| 初步评估与分级 | 14 天内 |
| 修复并发布 | 视严重程度，尽量 30 天内 |

修复发布后会在 [GitHub Security Advisories](https://docs.github.com/code-security/security-advisories) 与本仓库的 release notes 中说明并致谢（除非你希望匿名）。

## 部署者的责任

本项目是**自托管**工具，很多安全属性取决于部署方式。升级前请确认：

- `APP_SECRET` 已设置为随机值（`openssl rand -hex 32`），且**不要提交进 git**
- WebUI 不直接暴露公网，前面有反向代理 + HTTPS；并把真实代理地址写进 `TRUSTED_PROXIES`
  （否则限流与审计拿到的都是代理 IP，等于没有防护）
- 若不使用反向代理，务必设置强 `ADMIN_PASSWORD`，并尽快补上 HTTPS
  （无 HTTPS 时会话 Cookie 无法带 `Secure` 标记）
- KOOK Token 的权限按最小必要授予；`data/` 目录只允许运行用户读取（程序默认 0700/0600）
- 定期执行 `./deploy.sh backup`，并**实际验证过一次恢复流程**

## 已知的安全设计边界

这些是刻意为之的取舍，不是漏洞：

- **单服务器（单 guild）设计**。多租户隔离不在当前范围内。
- **WebUI 不提供「以机器人身份发言」**，对话在 KOOK 内进行。
- **KOOK Token 以 AES-GCM 加密入库**，密钥来自 `APP_SECRET`。拿到 `APP_SECRET` + 数据库文件的人可以解密 Token —— 请把两者当成同一等级的机密保护。
- **平台侧限流**（KOOK API 8 req/s 量级）会影响开单/关单耗时，日志中的 `elapsed` 变长属于正常现象，不代表被攻击。

## 不在范围内

以下情况通常不作为安全漏洞处理：

- 演示模式（`KOOK_DRYRUN=1`）与 `scripts/e2e/` 中的固定测试密码
- 需要攻击者已经拥有宿主机 root / 能读写 `data/` 目录的场景
- 缺少用户主动配置的安全加固（如自行关闭 HTTPS、把 `TRUSTED_PROXIES` 写成 `0.0.0.0/0`）
- 依赖项的漏洞报告本身 —— 请直接报给上游；本项目会跟进升级
