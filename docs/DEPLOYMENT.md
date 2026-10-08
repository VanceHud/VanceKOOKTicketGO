# 部署教程

本文从零开始，把 KOOK Ticket 机器人 + WebUI 部署到一台服务器上。

> 阅读顺序建议：先看 [第 1 步](#1-准备-kook-应用) 和 [第 2 步](#2-准备服务器与频道结构)（与 KOOK 账号强相关，只需做一次），
> 再按你选择的方式部署：[Docker Compose（推荐）](#3-方式-adocker-compose推荐) / [二进制 + systemd](#4-方式-b二进制--systemd) / [本地快速试跑](#5-方式-c本地快速试跑dry-run)。
> 部署完成后务必按 [第 7 步：首次配置顺序](#7-首次进入-webui-的配置顺序重要) 走一遍，否则工单功能不会生效。

---

## 0. 部署方式怎么选

| 方式 | 适合场景 | 需要什么 | 数据与升级 |
|---|---|---|---|
| **A. Docker Compose** | 云服务器、NAS、长期运行 | 已装 Docker（含 compose 插件） | 数据在 `./data` 卷；升级 = 重新构建镜像 |
| **B. 二进制 + systemd** | 不想引入 Docker、或已有 systemd 管理习惯 | Linux 服务器，无需额外运行时 | 数据在 `DATA_DIR`；升级 = 替换二进制 |
| **C. 本地快速试跑** | 先看界面、验证配置项 | 任意电脑 | 演示数据，可随时删除 |

三种方式产出的都是**同一个单文件程序**（前端已内嵌，无需 Nginx 之外的额外组件），数据库为 SQLite，无需单独安装数据库服务。

**服务器最低配置**：1 核 1G 内存、500MB 可用磁盘（SQLite + 日志足够；工单量很大时按聊天记录增长预留空间）。
**网络要求**：出网可访问 `www.kookapp.cn`（WebSocket + REST）。**不需要公网 IP，也不需要开放回调端口** —— 本项目使用 WebSocket 长连接对接 KOOK。

---

## 1. 准备 KOOK 应用

### 1.1 创建机器人并拿到 Token

1. 打开 [KOOK 开发者后台](https://developer.kookapp.cn/app/index)（需先登录 KOOK 账号）。
2. **创建应用** → 填写名称。
3. 进入应用 → 左侧 **机器人**：
   - 复制 **Token**（形如 `1/MTk5/xxxx==`），后面要填进 WebUI；
   - **连接方式选择 WebSocket**（本项目使用长连接；请**不要**开启 Webhook 回调，否则机器人收不到事件）。

### 1.2 订阅事件

在同一页面的 **事件** 区域，勾选以下事件（名称以控制台为准，含义如下）：

| 事件 | 用途 |
|---|---|
| 频道消息 | 归档工单频道聊天记录、接收 `/ticket`、`/tkcm`、`/tkclose`、`/aar` 等命令 |
| 私聊消息 | `/login`、`/bind` 一次性码 |
| 消息按钮点击 | 工单面板的「开单」、工单卡片里的「关闭 / 锁定 / 重新激活」 |
| 用户表情回应（added_reaction） | 表情上角色 |

### 1.3 给机器人开权限

在服务器里给**机器人自身角色**（服务器设置 → 角色管理 → 找到机器人的角色）勾选：

| 权限 | 为什么需要 |
|---|---|
| 查看频道 | 读取工单频道消息、拉取频道与角色列表 |
| 发送消息 | 发送面板卡片、工单卡片、关闭通知、私信 |
| 管理频道 | 创建工单频道、删除工单频道 |
| 管理角色权限（频道权限覆写） | 给工单频道下发「谁可见谁可发言」 |
| 管理角色 | 关闭工单时 @ 管理员角色；「表情上角色」需要发放角色 |
| 上传文件（可选） | 机器人发送图片类消息时才需要 |

> **角色位置很关键**：机器人角色必须排在所有需要它发放的角色**之上**，否则「表情上角色」会失败（接口返回无权限）。

### 1.4 邀请机器人进服务器

开发者后台 → 机器人 → 复制**邀请链接**（或使用「邀请」按钮），在浏览器中打开并选择目标服务器完成邀请。

---

## 2. 准备服务器与频道结构

### 2.1 创建隐藏分组（工单频道会建在这里）

工单频道应当只对开单人和管理员可见，所以需要：

1. KOOK 服务器 → 服务器设置 → 频道 → **新建分组**，命名如「工单」。
2. 该分组的权限设置：**@全体成员 → 勾选「分组不可见」**。
3. 给**管理员角色**与**机器人角色**勾选「查看分组」（以及分组内的「查看频道」「发送消息」）。
4. 复制该分组的 **ID**：先在 KOOK 客户端 `设置 → 高级设置 → 打开开发者模式`，再右键分组 → **复制 ID**。

> 机器人在线后，WebUI 的系统设置页可以直接从下拉里选择分组/频道，不必手工抄 ID（第 7 步会说明）。

### 2.2 创建日志与调试频道

| 频道 | 用途 | 可见范围建议 |
|---|---|---|
| 工单日志 | 每次工单关闭时发送记录卡片（含备注更新） | 仅管理员 |
| 机器人调试 | 机器人在建频道/权限失败时发送错误提示 | 仅管理员 |

同样右键复制这两个频道的 ID。

---

## 3. 方式 A：Docker Compose（推荐）

### 3.0 一键脚本（最省事）

仓库根目录提供了 `deploy.sh`，覆盖“生成 .env → 构建 → 启动 → 备份 → 升级 → 重置密码”的完整流程：

```bash
git clone <你的仓库地址> kook-ticket && cd kook-ticket
./deploy.sh doctor          # 可选：环境自检（Docker、端口占用、磁盘、sqlite3）
./deploy.sh up              # 自动生成 .env、构建镜像、启动、健康检查，并打印初始密码
./deploy.sh                 # 不带参数 = 交互菜单
```

| 命令 | 作用 |
|---|---|
| `init` | 只生成 `.env`（随机 `APP_SECRET`；`ADMIN_PASSWORD` 默认留空 → 程序生成并强制首登改密） |
| `up` | 构建镜像并启动（首次会自动 init），支持 `--port` / `--bind` / `--tz` / `--dry-run` |
| `upgrade` | 备份 → 可选 `git pull` → 重建镜像 → 重启 → 健康检查 → 打印版本变化 |
| `backup` / `restore [文件]` | 备份（含 `.env` 里的密钥）与恢复；自动保留最近 10 份 |
| `reset-password [用户名]` | 忘记密码时的救援（自动停服 → 一次性容器重置 → 起服） |
| `status` / `logs` / `config` | 状态与访问地址 / 跟踪日志 / 查看配置（敏感值打码） |
| `doctor` | 环境自检 |
| `stop` / `down` / `restart` | 停止 / 删除容器 / 重启 |

脚本已经处理了几个容易踩的坑：

* **数据目录属主**：Linux 上会把 `PUID/PGID` 写入 `.env` 并交给容器，避免 bind mount 权限导致“unable to open database file”重启循环；
* **恢复不换目录**：只替换 `data/` 里的文件（Docker 的 bind mount 绑定目录 inode，换目录会让容器继续读写旧数据）；
* **重置密码先停服**：避免两个进程同时写同一个 WAL 数据库（Docker Desktop 下会报 `disk I/O error`）；
* **备份包含密钥**：恢复时若 `APP_SECRET` 与备份不一致会明确告警，并把备份内的 `.env` 留在 `data/previous-*/deploy-env`。

如果想手工部署或了解每一步在做什么，继续看下面的分步说明。

### 3.1 准备代码与配置

```bash
# 服务器上安装 Docker（如已安装可跳过）
curl -fsSL https://get.docker.com | sh

# 取代码（或把项目目录上传到服务器）
git clone <你的仓库地址> kook-ticket
cd kook-ticket

# 生成配置
cp .env.example .env

# 生成加密密钥（用于加密存储 KOOK Token，务必保存好）
echo "APP_SECRET=$(openssl rand -hex 32)" >> .env

# 建议同时设置初始管理员密码；不设则首次启动随机生成并打印到日志
sed -i "s/^ADMIN_PASSWORD=$/ADMIN_PASSWORD=改成你自己的强密码/" .env
```

编辑 `.env` 时需要确认的几项：

```ini
PORT=9235                 # WebUI 端口
TICKET_TZ=Asia/Shanghai   # 业务时区（默认即北京时间，如无特殊需求不用改）
KOOK_TOKEN=               # 可留空，稍后在 WebUI 里填
COOKIE_SECURE=auto        # 走 HTTPS 反代时保持 auto 即可
TRUSTED_PROXIES=          # 用了反向代理再填（见第 6 步）
```

### 3.2 启动

```bash
docker compose up -d --build
docker compose ps          # 状态应为 Up (healthy)
docker compose logs -f kook-ticket
```

**关于端口**：默认 compose 只监听回环地址（`127.0.0.1:9235`），也就是只能从本机访问 —— 这是为了配合反向代理。
如果你没有反向代理、想直接用 `http://服务器IP:9235` 访问，把 `docker-compose.yml` 里的端口改成：

```yaml
    ports:
      - "${PORT:-9235}:9235"     # 去掉前面的 127.0.0.1:
```

> ⚠️ 直接暴露到公网时，请务必先设置强密码与 `APP_SECRET`，并尽快按第 6 步加上 HTTPS。
> 没有 HTTPS 的情况下会话 Cookie 无法带 `Secure` 标记，存在被中间人窃听的风险。

### 3.3 获取初始管理员密码

如果 `.env` 里设置了 `ADMIN_PASSWORD`，直接用它登录。

如果留空，密码是随机生成的，只在启动日志里出现一次：

```bash
docker compose logs kook-ticket | grep initial_password
# time=... msg=已生成初始管理员账号，请立即登录并修改密码 username=admin initial_password=xxxxxxxx note=登录后会被强制要求修改密码
```

浏览器打开 `http://服务器IP:9235`（或反向代理域名），用 `admin` + 上述密码登录。**首次登录会被强制要求修改密码**，改完才能使用其它功能。

### 3.4 常用命令

```bash
docker compose logs -f kook-ticket        # 跟踪日志
docker compose restart kook-ticket        # 重启
docker compose down                       # 停止并删除容器（数据保留在 ./data）
docker compose up -d --build              # 更新代码后重建并启动
```

### 3.5 升级

```bash
cd kook-ticket
tar czf backup-$(date +%F).tar.gz data/   # 先备份
git pull
docker compose up -d --build
docker compose logs -f kook-ticket        # 观察启动日志（数据库迁移会自动执行）
```

> 数据库结构升级由程序启动时自动完成（`AutoMigrate`），**不支持自动回退**，因此升级前请务必备份。

### 3.6 备份与恢复

**方式一：停服后整目录打包（最稳，包含 WAL）**

```bash
docker compose stop
tar czf backup-$(date +%F).tar.gz data/
docker compose start
```

**方式二：宿主机装有 sqlite3 时在线备份（不中断服务）**

```bash
sqlite3 ./data/ticket.db ".backup './data/backup-$(date +%F).db'"
```

**恢复**

```bash
docker compose stop
rm -f data/ticket.db data/ticket.db-wal data/ticket.db-shm
tar xzf backup-2026-01-05.tar.gz          # 或把 backup-xxx.db 改名为 data/ticket.db
docker compose start
```

> 注意 `data/app_secret` 也要一起备份：它是加密 KOOK Token 的密钥。
> 只丢 `ticket.db` 不丢 `app_secret` 时，重新填写 Token 即可恢复；
> 丢了 `app_secret` 则 Token 无法解密，需要在 WebUI 重新填写（历史数据不受影响）。

### 3.7 忘记管理员密码

用内置的救援命令重置（重置后首次登录需改密，且该账号所有会话会被吊销）：

```bash
# 随机生成新密码并打印
docker compose exec kook-ticket /app/kook-ticket -reset-password admin

# 或指定新密码
docker compose exec kook-ticket /app/kook-ticket -reset-password admin -password 'NewPass@2026x'
```

---

## 4. 方式 B：二进制 + systemd

### 4.1 交叉编译

在本地（开发机）执行，产物是**静态二进制**，可直接丢到服务器：

```bash
make dist
ls -lh bin/
# bin/kook-ticket-linux-amd64   28M   ← x86_64 服务器
# bin/kook-ticket-linux-arm64   26M   ← ARM 服务器（如 Oracle/华为云 ARM 实例）
# bin/kook-ticket-darwin-arm64  26M   ← Apple Silicon Mac
```

> 前端已经 `//go:embed` 打进二进制，因此**部署时不需要 Node、不需要 npm、不需要额外静态目录**，一个文件即可。

### 4.2 服务器目录与专用账号

```bash
sudo useradd --system --home /opt/kook-ticket --shell /usr/sbin/nologin kookticket
sudo mkdir -p /opt/kook-ticket/data
sudo chown -R kookticket:kookticket /opt/kook-ticket
sudo chmod 700 /opt/kook-ticket/data

# 上传二进制
scp bin/kook-ticket-linux-amd64 root@服务器IP:/opt/kook-ticket/kook-ticket
sudo chown kookticket:kookticket /opt/kook-ticket/kook-ticket
sudo chmod 755 /opt/kook-ticket/kook-ticket
```

### 4.3 环境变量文件

```bash
sudo tee /opt/kook-ticket/.env > /dev/null <<'EOF'
PORT=9235
DATA_DIR=/opt/kook-ticket/data
TICKET_TZ=Asia/Shanghai
LOG_LEVEL=info
COOKIE_SECURE=auto
# 首次启动前请替换为 `openssl rand -hex 32` 的输出
APP_SECRET=请替换为随机密钥
ADMIN_PASSWORD=请替换为强密码
EOF
sudo chmod 600 /opt/kook-ticket/.env
sudo chown kookticket:kookticket /opt/kook-ticket/.env
```

> `.env` 与 `app_secret` 都含敏感信息：权限设为 600、只允许服务账号读取。

### 4.4 systemd 服务

```bash
sudo tee /etc/systemd/system/kook-ticket.service > /dev/null <<'EOF'
[Unit]
Description=KOOK Ticket Bot & WebUI
Documentation=https://github.com/your/repo
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=kookticket
Group=kookticket
WorkingDirectory=/opt/kook-ticket
EnvironmentFile=/opt/kook-ticket/.env
ExecStart=/opt/kook-ticket/kook-ticket
Restart=always
RestartSec=5
TimeoutStopSec=20

# 加固：禁止提权、只允许写数据目录与临时目录
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
ReadWritePaths=/opt/kook-ticket/data
LimitNOFILE=8192

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now kook-ticket
systemctl status kook-ticket
journalctl -u kook-ticket -f
```

### 4.5 健康检查与升级

```bash
curl -s http://127.0.0.1:9235/healthz
# {"db":"ok","status":"ok","uptimeSeconds":5,"version":"..."}

# 升级：替换二进制后重启
sudo systemctl stop kook-ticket
tar czf /root/kook-ticket-backup-$(date +%F).tar.gz -C /opt/kook-ticket data
sudo cp kook-ticket.new /opt/kook-ticket/kook-ticket
sudo chown kookticket:kookticket /opt/kook-ticket/kook-ticket
sudo systemctl start kook-ticket
journalctl -u kook-ticket -n 50
```

### 4.6 忘记管理员密码

```bash
sudo -u kookticket env $(grep -v '^#' /opt/kook-ticket/.env | xargs) \
  /opt/kook-ticket/kook-ticket -reset-password admin
```

> 也可以在 systemd 环境下手动指定：`DATA_DIR=/opt/kook-ticket/data /opt/kook-ticket/kook-ticket -reset-password admin`
> （读取数据库只需要 `DATA_DIR`/`DB_PATH` 与 `APP_SECRET`）。

---

## 5. 方式 C：本地快速试跑（DryRun）

不需要 KOOK Token，程序会写入 30 条演示工单，方便先看界面：

```bash
KOOK_DRYRUN=1 ADMIN_PASSWORD='DemoTicket@2026' go run ./cmd/server
# 打开 http://127.0.0.1:9235，用 admin / DemoTicket@2026 登录
```

或者用 Docker：

```bash
docker run --rm -p 127.0.0.1:9235:9235 \
  -e KOOK_DRYRUN=1 -e ADMIN_PASSWORD='DemoTicket@2026' \
  -v "$PWD/data:/app/data" kook-ticket:local
```

DryRun 模式下所有工单操作只更新数据库与界面，**不会**在 KOOK 里创建频道或发送消息。

---

## 6. 反向代理与 HTTPS

推荐用 Caddy（自动申请证书）或 Nginx + Certbot。

### 6.1 Caddy（最简单）

```
ticket.example.com {
    reverse_proxy 127.0.0.1:9235
}
```

Caddy 默认会带上 `X-Forwarded-For` / `X-Forwarded-Proto`，并正确处理 SSE（实时推送）。

### 6.2 Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name ticket.example.com;

    ssl_certificate     /etc/letsencrypt/live/ticket.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/ticket.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:9235;
        proxy_http_version 1.1;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # 实时推送（SSE）必须关闭缓冲，否则页面不会自动刷新
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 1h;
    }
}

server {
    listen 80;
    server_name ticket.example.com;
    return 301 https://$host$request_uri;
}
```

### 6.3 反代相关的两个环境变量

```ini
# 只有来自这些地址的 X-Forwarded-* 头才会被采信（支持 CIDR，逗号分隔）
TRUSTED_PROXIES=127.0.0.1

# 走 HTTPS 后建议显式指定；auto 也会在识别到 HTTPS 时自动开启
COOKIE_SECURE=always
```

Docker 场景的取值：

| 代理位置 | 建议值 |
|---|---|
| 代理装在宿主机（Nginx/Caddy 直接在系统里） | `TRUSTED_PROXIES=127.0.0.1,::1` |
| 代理也是容器、与本服务在同一 compose 网络 | 填 Docker 网络网关，例如 `172.18.0.1`（用 `docker network inspect <网络名> -f '{{(index .IPAM.Config 0).Gateway}}'` 查询） |
| 不确定 | 先留空部署，登录一次后在「审计日志」里看记录的 IP：若显示的是代理 IP，再按上面两种方式之一补上 |

> 未配置 `TRUSTED_PROXIES` 时，程序**忽略** `X-Forwarded-For`，一律使用直连地址 —— 这是刻意的安全设计（防止伪造请求头绕过登录限流）。代价是审计日志里记录的是代理 IP。

### 6.4 验证

配好后访问 `https://ticket.example.com/healthz` 应返回 `{"status":"ok",...}`；
再登录 WebUI，看顶栏是否显示「实时同步中」（说明 SSE 通了）。

---

## 7. 首次进入 WebUI 的配置顺序（重要）

按以下顺序配置，每一步都能立刻验证：

| 步骤 | 位置 | 操作 | 怎么确认成功 |
|---|---|---|---|
| 1 | 登录页 | 用初始密码登录，**按提示修改密码**（未改密前除改密外都用不了） | 能进入仪表盘 |
| 2 | 系统设置 | 填 **KOOK 机器人 Token**（只写字段，页面只显示末四位） | 保存成功提示 |
| 3 | 系统设置 | 填 **服务器 ID**；机器人在线后，分组/日志频道/调试频道可直接从下拉选择 | 保存成功 |
| 4 | 机器人状态 | 点 **重新连接** | 连接状态变为「已连接」，能看到机器人名与服务器名 |
| 5 | 角色与权限 | 配置 **登录角色映射**（KOOK 角色 → 管理员/客服/只读），例如「客服组 → 客服」 | 列表出现映射行（这一步决定 KOOK 成员能否用 `/login` 登录） |
| 6 | 角色与权限 | 配置 **全局管理员角色**：拥有该角色的成员可处理/关闭工单 | 列表出现角色行 |
| 7 | 工单类型 | 点 **新建类型**（例如「账号与充值」），展开后点 **为该类型添加面板**，选择面板频道 | KOOK 对应频道出现一张按钮卡片 |
| 8 | KOOK 内验证 | 在服务器里发 `/tkhelp` 应收到帮助卡片；点击面板按钮应创建一个只有自己和管理员可见的工单频道，并在面板频道看到「仅自己可见」的完成提示与频道跳转链接 | WebUI「工单」列表出现新工单 |
| 9 | 表情规则（可选） | 在频道里发一张角色选择消息 → 右键复制消息 ID → 在「表情上角色」登记「表情 → 角色」 | 用户对该消息回应表情后获得角色 |

> 第 2、3 步也可以不用 WebUI：直接在 `.env`/compose 里设置 `KOOK_TOKEN`、`KOOK_GUILD_ID` 并重启。
> 环境变量优先于界面值，适合用配置管理工具批量部署。

**几个容易踩的点**

- **面板必须由机器人创建**：按钮里带有机器人签名，手工在 KOOK 卡片编辑器里做的卡片点了不会有反应。
- **`/ticket` 命令等价于建面板**：管理员也可以直接在某个频道发 `/ticket 类型名` 新建一张面板卡片；类型不存在时会自动创建。省略类型名则复用当前频道已有面板的类型，同一频道允许存在多张卡片，各自独立配置文案。
- **管理员角色挂在工单类型上**：`/aar @角色` 作用于当前频道面板所属的全部类型；在 WebUI「工单类型」页也可直接增删。类型停用后，其所有面板都会拒绝开单。
- **KOOK 成员登录 WebUI**：需先在 KOOK 私聊机器人发 `/login` 拿到 6 位登录码（5 分钟有效），再在登录页选「登录码」输入。未命中角色映射的账号会被拒绝。

---

## 8. 日常运维

### 8.1 日志

```bash
docker compose logs -f --tail=100 kook-ticket          # Docker
journalctl -u kook-ticket -f                           # systemd
```

日志是结构化文本，`LOG_LEVEL=debug` 可以看到 KOOK 接口调用与 DryRun 跳过的动作。
日志中**不会**出现 Token、密码等敏感值（Token 只以 `****abcd` 形式出现）。

### 8.2 关键接口

| 地址 | 说明 |
|---|---|
| `GET /healthz` | 健康检查（Docker healthcheck 用它）：`{"status":"ok","db":"ok",...}`，数据库异常时返回 503 |

### 8.3 数据文件

```
data/
├─ ticket.db        # SQLite（工单、聊天记录、备注、配置、账号、审计）
├─ ticket.db-wal    # WAL 日志（正常运行中会存在）
├─ ticket.db-shm
└─ app_secret       # 加密密钥（0600，务必备份）
```

权限：目录 0700，数据库与密钥 0600；时间戳一律以 UTC 存库，界面按 `TICKET_TZ`（默认北京时间）展示。

### 8.4 升级检查清单

1. 备份 `data/`（见 3.6）
2. 更新代码/替换二进制
3. 重启服务并观察日志有无迁移报错（旧版本的「面板 + 面板角色」会自动迁移为「工单类型 + 类型角色」，日志中会出现 `已为存量面板创建工单类型`）
4. 打开 WebUI 抽查：仪表盘能加载、工单列表有数据、机器人状态「已连接」；在「工单类型」页确认旧面板已各自挂到一个同名类型下
5. 在 KOOK 里点一次面板按钮，确认开单链路正常，工单频道名形如 `类型｜短编号｜昵称`，首条卡片包含「工单类型」

> 网关会话（`session_id` + `sn`）保存在数据库里：容器重建后机器人会自动带旧会话续传，
> 平台会把停机期间的事件补发过来，因此升级不会漏掉停机时的点击。

---

## 9. 排错速查

| 现象 | 可能原因 | 处理 |
|---|---|---|
| 启动失败：`数据库迁移失败: violates foreign key constraint` | 0.1.0 旧库的 `panel_roles` 外键挡住了 `panels` 表重建（已在后续版本修复） | 升级到包含该修复的版本；数据库未受影响，无需手工干预 |
| 启动日志出现 `尚未配置 KOOK Token` | 没填 Token | 系统设置填 Token → 重新连接；或设置 `KOOK_TOKEN` 后重启 |
| 机器人状态一直「未连接」 | Token 错、服务器 ID 错、无法出网 | 看「机器人状态」里的错误详情；在容器内 `nslookup www.kookapp.cn` 验证 DNS/出网 |
| 点面板按钮没反应 | 卡片不是机器人发的（无签名）；未订阅「消息按钮点击」事件 | 用 `/ticket` 或 WebUI 重建面板；检查开发者后台的事件订阅 |
| 点按钮后要等好几秒才完成 | 开单/关闭要调用多个 KOOK 接口；若某个限流桶额度不足而阻塞了整个客户端，或流程串行执行，都会表现为「点了没反应」 | 本版本已改为按桶限速 + 流程并发（见 README 第 8 节）；看日志 `工单已创建 ... elapsed=` 与 `channel_ms/card_ms/grants_ms/notice_ms`，以及是否出现 `KOOK 接口被限流`（含 `bucket`） |
| 日志反复出现 `握手失败：期望 HELLO(s=1)，收到 s=5` | 平台对续传失败的回复：`reconnect(s=5)` 携带 40106/40107/40108，说明本地落的 `session_id`/`sn` 已失效 | 本版本会识别握手阶段的 `s=5`，清空失效会话后以全新会话重连（日志会带上平台返回的 `code`/`err`）；升级后重启服务即可恢复，无需手工删数据 |
| 机器人状态「已连接」，但 KOOK 里点按钮/发消息都没反应（重启、升级后尤其常见） | 进程重启时没有带旧会话 resume，平台把事件继续投递给尚未过期的旧会话，新连接只能收到心跳、收不到事件 | 本版本已自动处理：启动时会读取数据库里的 `kook_gateway_session_id`/`kook_gateway_sn` 并续传；若仍无事件，在「机器人状态」点**重新连接**（会清空失效会话改用全新连接）。排查时可对比「已处理事件数 / 最近事件时间」是否长期不增长 |
| 开单失败，调试频道提示建频道失败 | 机器人缺「管理频道」权限；分组 ID 错；机器人看不到该分组 | 按 1.3 给权限；确认分组对机器人角色可见 |
| 用户点开单后没收到私信通知 | 用户未开启私聊 | 这是 KOOK 侧限制：用户需先私聊机器人任意一条消息。开单时不再发探测私信；关闭时若私信送不到，机器人会在日志频道提醒管理员人工转达 |
| 关闭工单时通知发不出 | 机器人缺「发送消息」权限，或日志频道不可见 | 给机器人角色可见日志频道 + 发送消息权限 |
| 「表情上角色」失败 | 机器人角色位置低于目标角色；缺「管理角色」 | 把机器人角色拖到目标角色之上 |
| 工单类型页提示「机器人未连接 KOOK，无法发送面板卡片」 | 建面板需要机器人发卡片（新建类型本身不需要） | 先完成第 7 步 4～5，再添加面板 |
| 公网访问后审计日志里 IP 都是同一个 | 反向代理场景未配 `TRUSTED_PROXIES` | 见 6.3 |
| 页面数据不自动刷新，顶栏显示「未连接」 | SSE 被代理缓冲 | Nginx 加 `proxy_buffering off;`（见 6.2） |
| 登录被锁：`尝试次数过多` | 触发了登录限流 | 等待 `LOGIN_LOCK_MINUTES`（默认 15 分钟）；或重启服务清空内存计数 |
| 时间显示与本地不一致 | 界面统一按业务时区渲染 | 属预期行为；如需改动设置 `TICKET_TZ` |
| `login-code` 提示无效或已过期 | 一次性码只能用一次、有效期 5 分钟 | 重新在 KOOK 私聊发 `/login` 获取 |
| 忘记管理员密码 | —— | 见 3.7 / 4.6 的 `-reset-password` |
| 数据库自检失败/写入报错 | 磁盘满、文件权限被改、WAL 损坏 | 检查磁盘与 `data/` 权限（0700/0600）；从备份恢复 |
| 容器启动即退出 | `.env` 校验失败（如 `APP_SECRET` 过短、`TICKET_TZ` 非法、`PORT` 非数字） | `docker compose logs` 里会给出明确原因 |
| 容器反复重启，日志报 `unable to open database file` / 权限不足 | bind mount 的 `data/` 属主与容器运行身份不一致（Linux 常见） | 用 `./deploy.sh up`（会自动写入 `PUID/PGID`）；或 `sudo chown -R 10001:10001 data` |
| `docker compose exec ... -reset-password` 报 `disk I/O error (522)` | 两个进程同时访问 bind mount 上的 WAL 库（Docker Desktop/virtiofs） | 用 `./deploy.sh reset-password`（会先停服再执行）；不要直接 exec |
| 恢复后数据看起来没变 | 手工 `mv data` 换目录会保留旧的挂载 inode | 用 `./deploy.sh restore`（只替换目录内文件）；或手工删除 `data/ticket.db*` 后再解压覆盖 |
| 备份恢复到新机器后 KOOK Token 失效 | 新机器的 `APP_SECRET` 与备份不同（Token 是加密存储的） | 备份内含 `.env`（`deploy-env`），从中取回 `APP_SECRET` 写入 `.env` 后重启 |

---

## 10. 安全加固清单

部署到公网前，建议逐条确认：

- [ ] `APP_SECRET` 为随机值（`openssl rand -hex 32`）并已备份；`.env` 权限 600
- [ ] 初始管理员密码已修改（首次登录会强制要求）
- [ ] 已通过反向代理启用 HTTPS，并设置 `COOKIE_SECURE=always`（或 `auto`）
- [ ] `TRUSTED_PROXIES` 只包含真实代理地址，不要写成 `0.0.0.0/0`
- [ ] 容器/服务以非 root 运行（本项目默认如此：容器 uid 10001；systemd 用 `kookticket` 账号）
- [ ] 日志与审计中不出现 Token（界面/日志只显示掩码）
- [ ] 定期备份 `data/`，并验证过恢复流程
- [ ] 按需收敛暴露面：只开放 443 给公网，9235 仅本机监听
- [ ] KOOK 侧权限最小化：只给机器人必需的权限，管理员角色只给必要的人
- [ ] 定期查看「审计日志」中的异常登录与权限变更

---

## 11. 附录

### 11.1 环境变量速查

完整说明见仓库根目录 `.env.example`，常用项：

| 变量 | 默认 | 说明 |
|---|---|---|
| `PORT` | `9235` | WebUI 监听端口 |
| `DATA_DIR` | `./data` | 数据目录 |
| `TICKET_TZ` | `Asia/Shanghai` | 业务时区（编号日期段、统计口径、界面展示） |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `KOOK_DRYRUN` | `0` | `1` = 演示模式，不连 KOOK |
| `KOOK_TOKEN` / `KOOK_GUILD_ID` | 空 | 也可在 WebUI 里填；环境变量优先 |
| `APP_SECRET` | 自动生成 | 原始输入至少 32 字节，建议 `openssl rand -hex 32`；留空则写入 `DATA_DIR/app_secret` |
| `COOKIE_SECURE` | `auto` | `auto` / `always` / `never` |
| `TRUSTED_PROXIES` | 空 | 可信反代地址（CIDR 逗号分隔） |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | `admin` / 随机 | 仅首次初始化生效 |
| `SESSION_IDLE_HOURS` / `SESSION_MAX_DAYS` | `12` / `7` | 会话空闲与绝对过期 |
| `LOGIN_MAX_FAILS` / `LOGIN_WINDOW_MINUTES` / `LOGIN_LOCK_MINUTES` | `5` / `15` / `15` | 登录限流（锁定时长指数退避，封顶 1 小时） |
| `AUDIT_RETENTION_DAYS` | `180` | 审计日志保留期（后台每天清理；`0` = 永久保留） |

### 11.2 KOOK ID 获取方式

1. KOOK 客户端 → `设置 → 高级设置 → 打开开发者模式`
2. 右键 **服务器头像** → 复制服务器 ID
3. 右键 **分组 / 频道** → 复制频道 ID
4. 右键 **用户头像** → 复制用户 ID
5. 角色 ID：服务器设置 → 角色管理 → 右键角色 → 复制角色 ID
6. 消息 ID：右键消息 → 复制消息 ID（配置「表情上角色」用）

### 11.3 常用命令

```bash
# 查看初始密码
docker compose logs kook-ticket | grep initial_password

# 重置管理员密码
docker compose exec kook-ticket /app/kook-ticket -reset-password admin

# 查看版本
docker compose exec kook-ticket /app/kook-ticket -version

# 健康检查
curl -s http://127.0.0.1:9235/healthz

# 备份
tar czf backup-$(date +%F).tar.gz data/
```
