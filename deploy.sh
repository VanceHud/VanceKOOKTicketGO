#!/usr/bin/env bash
#
# KOOK Ticket 部署脚本
#
#   ./deploy.sh init            生成 .env（随机密钥与初始管理员密码）
#   ./deploy.sh up              构建镜像并启动（首次会自动 init）
#   ./deploy.sh upgrade         备份 → 重建镜像 → 重启 → 健康检查
#   ./deploy.sh status          查看状态、健康、版本与访问地址
#   ./deploy.sh logs            跟踪日志
#   ./deploy.sh backup          备份 data/ 到 backups/
#   ./deploy.sh restore <文件>   从备份恢复
#   ./deploy.sh reset-password  重置 WebUI 管理员密码
#   ./deploy.sh doctor          环境自检
#
# 不带参数运行会进入交互菜单。所有命令都可通过 --help 查看选项。
#
set -Eeuo pipefail

# ---------------------------------------------------------------------------
# 基础变量与输出
# ---------------------------------------------------------------------------

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

ENV_FILE=".env"
BACKUP_DIR="backups"
SERVICE="kook-ticket"
CONTAINER="kook-ticket"
KEEP_BACKUPS=10
DATA_DIR="data"

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_INFO=$'\033[36m'; C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_ERR=$'\033[31m'; C_BOLD=$'\033[1m'
else
  C_RESET=''; C_INFO=''; C_OK=''; C_WARN=''; C_ERR=''; C_BOLD=''
fi

log()  { printf '%s[信息]%s %s\n' "$C_INFO" "$C_RESET" "$*"; }
ok()   { printf '%s[完成]%s %s\n' "$C_OK" "$C_RESET" "$*"; }
warn() { printf '%s[警告]%s %s\n' "$C_WARN" "$C_RESET" "$*" >&2; }
die()  { printf '%s[错误]%s %s\n' "$C_ERR" "$C_RESET" "$*" >&2; exit 1; }
step() { printf '\n%s==> %s%s\n' "$C_BOLD" "$*" "$C_RESET"; }

trap 'die "脚本在第 $LINENO 行失败，请把上面的信息发出来以便排查"' ERR

# ---------------------------------------------------------------------------
# Docker 探测
# ---------------------------------------------------------------------------

# 注意：这里刻意不用数组保存命令。
# macOS 自带 bash 3.2，在 `set -u` 下展开空数组（"${arr[@]}"）会直接报 unbound variable，
# 因此改用“可选的 sudo 前缀 + compose 命令字符串”的写法，兼容 bash 3.2。
SUDO_BIN=""          # 空字符串或 "sudo"
COMPOSE_BIN="docker compose"

detect_docker() {
  command -v docker >/dev/null 2>&1 || die "未找到 docker 命令。请先安装：curl -fsSL https://get.docker.com | sh"

  if docker info >/dev/null 2>&1; then
    SUDO_BIN=""
  elif command -v sudo >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then
    SUDO_BIN="sudo"
    warn "当前用户不在 docker 组，将通过 sudo 调用 Docker（建议执行：sudo usermod -aG docker \$USER 后重新登录）"
  else
    die "无法连接 Docker 守护进程。请确认 Docker 已启动（systemctl start docker），或当前用户有权限访问 /var/run/docker.sock"
  fi

  # 优先使用 compose v2 插件（docker compose），回退到 v1（docker-compose）
  if ${SUDO_BIN} docker compose version >/dev/null 2>&1; then
    COMPOSE_BIN="docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_BIN="docker-compose"
    warn "检测到旧版 docker-compose（v1），建议升级到 Docker 自带的 compose 插件"
  else
    die "未找到 docker compose。请安装 Docker Compose 插件或 docker-compose"
  fi
  if [[ -n "$SUDO_BIN" ]]; then
    COMPOSE_BIN="${SUDO_BIN} ${COMPOSE_BIN}"
  fi
}

# compose 调用（自动带 sudo 前缀，若需要）
compose() { ${COMPOSE_BIN} "$@"; }
# 直接调用 docker（用于 inspect/exec/ps 等）
docker_cli() { ${SUDO_BIN} docker "$@"; }

# ---------------------------------------------------------------------------
# 小工具
# ---------------------------------------------------------------------------

rand_hex() {
  local bytes="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
  else
    od -An -tx1 -N "$bytes" /dev/urandom | tr -d ' \n'
  fi
}

rand_uint() { od -An -tu4 -N4 /dev/urandom | tr -d ' \n'; }

# gen_password 生成 16 位强密码。
#
# 字符集与应用内 GeneratePassword 保持一致：刻意排除 = " ' \ $ ` % 与空格，
# 避免在 .env、systemd EnvironmentFile 或 shell 中出现转义问题。
gen_password() {
  local upper='ABCDEFGHJKLMNPQRSTUVWXYZ'
  local lower='abcdefghijkmnopqrstuvwxyz'
  local digits='23456789'
  local symbols='!@#^*()-_+.:?~'
  local all="${upper}${lower}${digits}${symbols}"
  local out='' set char i j tmp

  for set in "$upper" "$lower" "$digits" "$symbols"; do
    char="${set:$(( $(rand_uint) % ${#set} )):1}"
    out+="$char"
  done
  while (( ${#out} < 16 )); do
    out+="${all:$(( $(rand_uint) % ${#all} )):1}"
  done

  # Fisher–Yates 洗牌，避免“前四位固定每类一个”的可预测前缀
  local -a chars=()
  for (( i = 0; i < ${#out}; i++ )); do chars+=("${out:i:1}"); done
  for (( i = ${#chars[@]} - 1; i > 0; i-- )); do
    j=$(( $(rand_uint) % (i + 1) ))
    tmp="${chars[i]}"; chars[i]="${chars[j]}"; chars[j]="$tmp"
  done
  local result=''
  for char in "${chars[@]}"; do result+="$char"; done
  printf '%s' "$result"
}

env_value() {
  local key="$1" def="${2:-}" value
  value="$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
  if [[ -n "$value" ]]; then printf '%s' "$value"; else printf '%s' "$def"; fi
}

set_env_value() {
  local key="$1" value="$2" tmp
  tmp="$(mktemp)"
  if grep -qE "^${key}=" "$ENV_FILE"; then
    # 用 awk 替换，避免值中的 / 破坏 sed 表达式
    awk -v key="$key" -v val="$value" '
      BEGIN { FS = "="; done = 0 }
      $0 ~ "^" key "=" && !done { print key "=" val; done = 1; next }
      { print }
    ' "$ENV_FILE" > "$tmp"
  else
    cat "$ENV_FILE" > "$tmp"
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
  fi
  cat "$tmp" > "$ENV_FILE"
  rm -f "$tmp"
}

mask() {
  local value="$1"
  if [[ -z "$value" ]]; then printf '(空)'
  elif (( ${#value} <= 4 )); then printf '****'
  else printf '****%s' "${value: -4}"
  fi
}

confirm() {
  local prompt="$1"
  [[ "${ASSUME_YES:-0}" == "1" ]] && return 0
  [[ -t 0 ]] || return 0
  read -r -p "$prompt [y/N] " reply
  [[ "$reply" =~ ^[Yy]$ ]]
}

ensure_env() {
  [[ -f "$ENV_FILE" ]] || die "未找到 .env。请先执行：./deploy.sh init"
}

# ---------------------------------------------------------------------------
# .env 生成
# ---------------------------------------------------------------------------

cmd_init() {
  local port="${OPT_PORT:-8080}"
  local bind="${OPT_BIND:-127.0.0.1}"
  local tz="${OPT_TZ:-Asia/Shanghai}"
  local dry="${OPT_DRYRUN:-0}"
  local password="${OPT_PASSWORD:-}"

  if [[ -f "$ENV_FILE" && "${OPT_FORCE:-0}" != "1" ]]; then
    warn ".env 已存在，未做修改。"
    log "如需重新生成请加 --force（会覆盖现有配置，数据库与账号不受影响）"
    log "如需只改端口/监听地址：./deploy.sh up --port 8081 --bind 0.0.0.0"
    return 0
  fi

  step "生成 ${ENV_FILE}"
  local app_secret password_source="程序随机生成（首登强制改密）"
  app_secret="$(rand_hex 32)"
  # 默认不把明文密码写进 .env：留空时程序会生成随机密码、打印在启动日志里，并要求首登改密。
  # 只有显式传入 --password 时才写入 .env（此时不会再强制改密）。
  if [[ -n "$password" ]]; then
    password_source="使用 --password 指定（不会强制改密）"
  fi

  local puid pgid
  puid="$(id -u)"
  pgid="$(id -g)"

  local tmp
  tmp="$(mktemp)"
  {
    printf '# 由 deploy.sh 生成于 %s\n' "$(date '+%Y-%m-%d %H:%M:%S')"
    printf '# 该文件含密钥，请勿提交到仓库；修改后执行 ./deploy.sh up 生效。\n'
    printf '\n# ---- 基础 ----\n'
    printf 'PORT=%s\n' "$port"
    printf 'BIND_ADDR=%s\n' "$bind"
    printf 'TICKET_TZ=%s\n' "$tz"
    printf 'LOG_LEVEL=info\n'
    printf '\n# ---- 容器运行身份（Linux 上需与 data 目录属主一致）----\n'
    printf 'PUID=%s\n' "$puid"
    printf 'PGID=%s\n' "$pgid"
    printf '\n# ---- 安全 ----\n'
    printf '# 加密 KOOK Token 的密钥；更换后已存 Token 需在 WebUI 重新填写\n'
    printf 'APP_SECRET=%s\n' "$app_secret"
    printf '# auto=按请求是否 HTTPS 自动判断；走反向代理也可显式设为 always\n'
    printf 'COOKIE_SECURE=auto\n'
    printf '# 可信反向代理地址（CIDR，逗号分隔）。未配置时忽略 X-Forwarded-For\n'
    printf 'TRUSTED_PROXIES=\n'
    printf '\n# ---- 初始管理员（仅数据库无账号时生效）----\n'
    printf '# ADMIN_PASSWORD 留空 = 首次启动随机生成并打印在日志中，且首次登录强制改密（推荐）\n'
    printf '# 若在此填写密码，则不会被强制改密；弱密码会导致启动失败。\n'
    printf 'ADMIN_USERNAME=admin\n'
    printf 'ADMIN_PASSWORD=%s\n' "$password"
    printf '\n# ---- KOOK 接入（也可留空，稍后在 WebUI 里填）----\n'
    printf 'KOOK_DRYRUN=%s\n' "$dry"
    printf 'KOOK_TOKEN=\n'
    printf 'KOOK_GUILD_ID=\n'
    printf '\n# ---- 会话与登录保护 ----\n'
    printf 'SESSION_IDLE_HOURS=12\n'
    printf 'SESSION_MAX_DAYS=7\n'
    printf 'LOGIN_MAX_FAILS=5\n'
    printf 'LOGIN_WINDOW_MINUTES=15\n'
    printf 'LOGIN_LOCK_MINUTES=15\n'
  } > "$tmp"
  mv "$tmp" "$ENV_FILE"
  chmod 600 "$ENV_FILE"

  ok "已生成 ${ENV_FILE}（权限 600）"
  log "宿主机端口：$port   监听地址：$bind   业务时区：$tz   运行身份：$puid:$pgid"
  log "初始管理员密码：${password_source}"
}

# show_initial_password 从启动日志里取出首次生成的初始密码并提示。
#
# 说明：密码只在首次初始化时打印一次，取不到就说明数据库里已有账号。
show_initial_password() {
  [[ -z "$(env_value ADMIN_PASSWORD '')" ]] || return 0
  local line
  line="$(compose logs --tail=300 "$SERVICE" 2>/dev/null | grep -o 'initial_password=[^ "]*' | tail -1 || true)"
  [[ -n "$line" ]] || return 0
  printf '\n%s初始管理员密码%s\n  用户名：%s\n  密  码：%s\n\n' \
    "$C_BOLD" "$C_RESET" "$(env_value ADMIN_USERNAME admin)" "${line#initial_password=}"
  warn "该密码只打印一次，请在首次登录后立即改密（系统会强制要求）"
}

# ---------------------------------------------------------------------------
# 构建与启动
# ---------------------------------------------------------------------------

cmd_up() {
  [[ -f "$ENV_FILE" ]] || cmd_init

  if [[ -n "${OPT_PORT:-}" ]]; then set_env_value PORT "$OPT_PORT"; log "已更新端口为 $OPT_PORT"; fi
  if [[ -n "${OPT_BIND:-}" ]]; then set_env_value BIND_ADDR "$OPT_BIND"; log "已更新监听地址为 $OPT_BIND"; fi
  if [[ -n "${OPT_TZ:-}" ]]; then set_env_value TICKET_TZ "$OPT_TZ"; log "已更新业务时区为 $OPT_TZ"; fi
  if [[ -n "${OPT_DRYRUN:-}" ]]; then set_env_value KOOK_DRYRUN "$OPT_DRYRUN"; fi

  mkdir -p "$DATA_DIR"
  chmod 700 "$DATA_DIR" 2>/dev/null || true
  # Linux 上把数据目录交给容器内的运行身份，避免 bind mount 写不进去
  if [[ "$(uname -s)" == "Linux" ]]; then
    local puid pgid
    puid="$(env_value PUID "$(id -u)")"; pgid="$(env_value PGID "$(id -g)")"
    if [[ "$(stat -c '%u:%g' "$DATA_DIR" 2>/dev/null || echo '')" != "$puid:$pgid" ]]; then
      if [[ "$(id -u)" == "0" ]]; then chown -R "$puid:$pgid" "$DATA_DIR"
      else warn "data 目录属主与容器运行身份（$puid:${pgid}）不一致，若容器反复重启请执行：sudo chown -R $puid:$pgid $DATA_DIR"; fi
    fi
  fi

  step "构建镜像并启动容器"
  compose up -d --build --remove-orphans

  wait_ready
  cmd_status
  show_initial_password
}

cmd_restart() {
  ensure_env
  step "重启容器"
  compose restart
  wait_ready
}

cmd_stop() {
  ensure_env
  step "停止容器（数据保留在 ./${DATA_DIR}）"
  compose stop
  ok "已停止"
}

cmd_down() {
  ensure_env
  step "停止并删除容器（数据保留在 ./${DATA_DIR}）"
  compose down --remove-orphans
  ok "已删除容器；下次 ./deploy.sh up 会用同一份数据重新创建"
}

wait_ready() {
  local timeout="${1:-120}" elapsed=0 url status
  url="http://127.0.0.1:$(env_value PORT 8080)/healthz"

  step "等待服务就绪（最长 ${timeout}s）"
  while (( elapsed < timeout )); do
    status="$(docker_cli inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER" 2>/dev/null || echo missing)"
    if [[ "$status" == "healthy" ]]; then
      ok "容器健康检查通过"
      return 0
    fi
    if [[ "$status" == "missing" ]]; then
      break
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done

  if curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
    ok "服务可访问：$url"
    return 0
  fi

  warn "服务未在 ${timeout}s 内就绪。最近日志："
  compose logs --tail=40 "$SERVICE" || true
  die "启动失败，请按上面的日志排查（部署教程：docs/DEPLOYMENT.md 第 9 节排错速查）"
}

# ---------------------------------------------------------------------------
# 升级
# ---------------------------------------------------------------------------

cmd_upgrade() {
  ensure_env

  step "升级前检查"
  local before_version=""
  before_version="$(docker_cli exec "$CONTAINER" /app/kook-ticket -version 2>/dev/null || true)"
  [[ -n "$before_version" ]] && log "当前版本：$before_version" || log "当前版本：未知（容器可能未在运行）"

  if [[ "${OPT_NOBACKUP:-0}" != "1" ]]; then
    cmd_backup
  else
    warn "已按 --no-backup 跳过备份"
  fi

  if [[ -d .git ]]; then
    if [[ -n "$(git status --porcelain 2>/dev/null || true)" ]]; then
      warn "工作区有未提交改动，跳过 git pull（镜像仍会用当前代码重新构建）"
    else
      step "拉取最新代码"
      local remote="${GIT_REMOTE:-origin}" branch
      branch="$(git rev-parse --abbrev-ref HEAD)"
      if git pull --ff-only "$remote" "$branch"; then
        ok "代码已更新到 $(git rev-parse --short HEAD)"
      else
        warn "git pull 失败（可能没有远端或网络不通），继续使用当前代码构建"
      fi
    fi
  fi

  step "重新构建镜像并滚动重启"
  compose up -d --build --remove-orphans
  wait_ready

  local after_version=""
  after_version="$(docker_cli exec "$CONTAINER" /app/kook-ticket -version 2>/dev/null || true)"
  ok "升级完成：${before_version:-未知} → ${after_version:-未知}"
  log "数据库结构由程序启动时自动迁移；如新版本异常，可用 ./deploy.sh restore $(latest_backup 2>/dev/null || echo '<备份文件>') 回滚数据"
}

# ---------------------------------------------------------------------------
# 状态
# ---------------------------------------------------------------------------

cmd_status() {
  ensure_env

  local port bind version health
  port="$(env_value PORT 8080)"
  bind="$(env_value BIND_ADDR 127.0.0.1)"
  version="$(docker_cli exec "$CONTAINER" /app/kook-ticket -version 2>/dev/null || true)"
  health="$(curl -fsS --max-time 5 "http://127.0.0.1:${port}/healthz" 2>/dev/null || true)"

  step "服务状态"
  compose ps || true

  printf '\n%s访问地址%s  http://%s:%s\n' "$C_BOLD" "$C_RESET" "$([[ "$bind" == "0.0.0.0" ]] && echo '<服务器IP>' || echo '127.0.0.1')" "$port"
  printf '%s运行版本%s  %s\n' "$C_BOLD" "$C_RESET" "${version:-未知}"
  printf '%s健康检查%s  %s\n' "$C_BOLD" "$C_RESET" "${health:-未就绪}"
  printf '%s业务时区%s  %s\n' "$C_BOLD" "$C_RESET" "$(env_value TICKET_TZ Asia/Shanghai)"
  printf '%s数据目录%s  %s（大小 %s）\n' "$C_BOLD" "$C_RESET" "$DATA_DIR" "$(du -sh "$DATA_DIR" 2>/dev/null | cut -f1 || echo '—')"
  printf '%s演示模式%s  %s\n' "$C_BOLD" "$C_RESET" "$([[ "$(env_value KOOK_DRYRUN 0)" == "1" ]] && echo '开启（不连接 KOOK）' || echo '关闭')"

  if [[ -z "$health" ]]; then
    printf '\n'; warn "健康检查未通过，最近的日志："
    compose logs --tail=20 "$SERVICE" || true
  fi
}

cmd_logs() {
  ensure_env
  log "跟踪日志（Ctrl+C 退出）"
  compose logs -f --tail=200 "$SERVICE"
}

# ---------------------------------------------------------------------------
# 备份与恢复
# ---------------------------------------------------------------------------

latest_backup() {
  ls -1t "$BACKUP_DIR"/*.tar.gz 2>/dev/null | head -1
}

cmd_backup() {
  ensure_env
  mkdir -p "$BACKUP_DIR"

  local stamp file stage online=0 suffix=0
  stamp="$(date '+%Y%m%d-%H%M%S')"
  file="$BACKUP_DIR/kook-ticket-$stamp.tar.gz"
  # 同一秒内可能触发多次备份（例如 upgrade 又会备份一次）：
  # 时间戳精度不够会互相覆盖，这里追加序号避免丢失快照。
  while [[ -e "$file" ]]; do
    suffix=$((suffix + 1))
    file="$BACKUP_DIR/kook-ticket-$stamp-$suffix.tar.gz"
  done

  [[ -d "$DATA_DIR" ]] || die "数据目录 $DATA_DIR 不存在，无需备份"

  step "备份数据"
  stage="$(mktemp -d)"
  mkdir -p "$stage/$DATA_DIR"

  local was_running=0
  if docker_cli ps --filter "name=^/${CONTAINER}$" --filter "status=running" -q | grep -q .; then
    was_running=1
  fi

  # 装了 sqlite3 时用官方 .backup 做在线备份（不中断机器人）
  if command -v sqlite3 >/dev/null 2>&1 && [[ -f "$DATA_DIR/ticket.db" ]] && [[ "$was_running" == "1" ]]; then
    log "使用 sqlite3 在线备份数据库（服务不中断）"
    if sqlite3 "$DATA_DIR/ticket.db" ".backup '$stage/$DATA_DIR/ticket.db'"; then
      online=1
    else
      warn "在线备份失败，改为停服冷备"
      rm -f "$stage/$DATA_DIR/ticket.db"
    fi
  fi

  if [[ "$online" != "1" ]]; then
    local paused=0
    if [[ "$was_running" == "1" ]]; then
      paused=1
      log "暂停容器以保证数据一致（几秒钟）"
      compose stop "$SERVICE" >/dev/null
    fi
    for f in "$DATA_DIR"/ticket.db*; do
      [[ -f "$f" ]] && cp -p "$f" "$stage/$DATA_DIR/"
    done
    if [[ "$paused" == "1" ]]; then
      compose start "$SERVICE" >/dev/null
      log "容器已恢复运行"
    fi
  fi

  # 二进制部署时密钥位于数据目录
  if [[ -f "$DATA_DIR/app_secret" ]]; then
    cp -p "$DATA_DIR/app_secret" "$stage/$DATA_DIR/app_secret"
  fi

  # .env 必须一起备份：容器的 APP_SECRET 在那里，
  # 只恢复数据库而没有密钥，数据库里加密的 KOOK Token 将再也无法解密。
  local env_included=0
  if [[ -f "$ENV_FILE" ]]; then
    cp -p "$ENV_FILE" "$stage/deploy-env"
    env_included=1
  fi

  if [[ "$env_included" == "1" ]]; then
    tar czf "$file" -C "$stage" "$DATA_DIR" deploy-env
  else
    tar czf "$file" -C "$stage" "$DATA_DIR"
  fi
  rm -rf "$stage"

  ok "备份完成：${file}（$(du -h "$file" | cut -f1)）"
  log "备份内容：数据库与密钥（deploy-env），请与备份一同保管"

  # 只保留最近 N 份
  local count
  count="$(ls -1 "$BACKUP_DIR"/*.tar.gz 2>/dev/null | wc -l | tr -d ' ')"
  if (( count > KEEP_BACKUPS )); then
    ls -1t "$BACKUP_DIR"/*.tar.gz | tail -n +$((KEEP_BACKUPS + 1)) | while read -r old; do
      rm -f "$old"
      log "已清理旧备份：$old"
    done
  fi
}

cmd_restore() {
  ensure_env
  local file="${1:-}"
  if [[ -z "$file" ]]; then
    file="$(latest_backup || true)"
    [[ -n "$file" ]] || die "未指定备份文件，且 $BACKUP_DIR/ 下没有可用备份"
    log "未指定文件，使用最新备份：$file"
  fi
  [[ -f "$file" ]] || die "备份文件不存在：$file"

  confirm "恢复会用备份覆盖当前数据（建议先执行 ./deploy.sh backup），继续？" || die "已取消"
  step "恢复数据"

  # 关键：只替换 data 目录**里面的文件**，绝不动目录本身。
  # Docker（尤其 Docker Desktop）在容器创建时绑定的是目录 inode；
  # 若把整个目录 mv 走再新建，容器会继续读写被改名的旧目录，导致“恢复看起来成功、实际没生效”。
  [[ -d "$DATA_DIR" ]] || mkdir -p "$DATA_DIR"

  if docker_cli ps --filter "name=^/${CONTAINER}$" -q | grep -q .; then
    compose stop "$SERVICE" >/dev/null
    log "已停止容器"
  fi

  local stamp aside stage
  stamp="$(date '+%Y%m%d-%H%M%S')"
  aside="$DATA_DIR/previous-$stamp"
  mkdir -p "$aside"
  # 移动（而非删除）现有数据文件，便于失败时人工回退
  find "$DATA_DIR" -maxdepth 1 -type f \( -name 'ticket.db*' -o -name 'app_secret' \) -exec mv {} "$aside"/ \; 2>/dev/null || true
  log "原数据文件已移至 ${aside}/"

  stage="$(mktemp -d)"
  if ! tar xzf "$file" -C "$stage"; then
    rm -rf "$stage"
    mv "$aside"/* "$DATA_DIR"/ 2>/dev/null || true
    die "备份文件解压失败，已回退到原数据"
  fi

  local src="$stage/$DATA_DIR"
  [[ -d "$src" ]] || src="$stage"
  local restored=0 f
  for f in "$src"/*; do
    [[ -f "$f" ]] || continue
    cp -p "$f" "$DATA_DIR/" && restored=$((restored + 1))
  done

  (( restored > 0 )) || { rm -rf "$stage"; die "备份中未找到可用数据文件，请检查备份完整性"; }

  # 备份里的 APP_SECRET 必须与当前一致，否则数据库内加密的 KOOK Token 无法解密
  local backup_secret="" current_secret=""
  backup_secret="$(grep -E '^APP_SECRET=' "$stage/deploy-env" 2>/dev/null | cut -d= -f2- || true)"
  current_secret="$(env_value APP_SECRET '')"
  if [[ -n "$backup_secret" && -n "$current_secret" && "$backup_secret" != "$current_secret" ]]; then
    # 把密钥副本留在持久位置（临时目录马上会被删除，提示里的路径必须可用）
    cp -p "$stage/deploy-env" "$aside/deploy-env" 2>/dev/null || true
    warn "备份中的 APP_SECRET 与当前 .env 不一致：数据库里加密的 KOOK Token 将无法解密"
    log "备份内的 .env 已保存为 ${aside}/deploy-env"
    log "如需改用备份中的密钥：cp ${aside}/deploy-env $ENV_FILE 然后 ./deploy.sh up"
  fi
  rm -rf "$stage"

  chmod 700 "$DATA_DIR" 2>/dev/null || true
  find "$DATA_DIR" -maxdepth 1 -type f -exec chmod 600 {} \; 2>/dev/null || true

  compose start "$SERVICE" >/dev/null 2>&1 || compose up -d --remove-orphans
  wait_ready

  ok "恢复完成（还原 ${restored} 个文件）"
  log "确认数据无误后可删除 ${aside}/"
}

# ---------------------------------------------------------------------------
# 运维辅助
# ---------------------------------------------------------------------------

# reset-password 使用「停服 → 一次性容器执行 → 起服」的流程。
#
# 为什么不直接用 docker compose exec：
#   那会在同一个 bind-mounted 数据库上同时存在两个进程（服务 + 命令），
#   在 Docker Desktop（virtiofs）下打开 WAL 库会报 disk I/O error (522)。
#   停服后用一次性容器执行是单写入方，稳定且语义更清晰。
cmd_reset_password() {
  ensure_env
  local user="${1:-$(env_value ADMIN_USERNAME admin)}"
  local password="${OPT_PASSWORD:-}"
  local was_running=0

  if docker_cli ps --filter "name=^/${CONTAINER}$" --filter "status=running" -q | grep -q .; then
    was_running=1
  fi

  step "重置账号 ${user} 的密码"
  if [[ "$was_running" == "1" ]]; then
    log "暂停服务以避免两个进程同时写入数据库"
    compose stop "$SERVICE" >/dev/null
  fi

  local args=(-reset-password "$user")
  [[ -n "$password" ]] && args+=(-password "$password")

  local exit_code=0
  compose run --rm -T "$SERVICE" "${args[@]}" || exit_code=$?

  if [[ "$was_running" == "1" ]]; then
    compose start "$SERVICE" >/dev/null
    wait_ready
  fi

  (( exit_code == 0 )) || die "重置失败（退出码 ${exit_code}）"
  ok "重置完成；该账号所有会话已失效，首次登录需修改密码"
}

cmd_config() {
  ensure_env
  step "$ENV_FILE 当前内容（敏感值已打码）"
  local key value
  while IFS='=' read -r key value; do
    case "$key" in
      ''|\#*) printf '%s\n' "$key=$value"; continue ;;
    esac
    case "$key" in
      APP_SECRET|ADMIN_PASSWORD|KOOK_TOKEN)
        printf '%s=%s\n' "$key" "$(mask "$value")" ;;
      *)
        printf '%s=%s\n' "$key" "$value" ;;
    esac
  done < "$ENV_FILE"
  printf '\n（如需查看明文：cat %s，请勿外传）\n' "$ENV_FILE"
}

cmd_doctor() {
  step "环境自检"

  local failed=0
  command -v docker >/dev/null 2>&1 && ok "docker 已安装：$(docker --version)" || { warn "未安装 docker"; failed=1; }

  if detect_docker 2>/dev/null; then
    ok "Docker 守护进程可访问；compose：$( ${COMPOSE_BIN} version --short 2>/dev/null || echo '未知')"
  else
    warn "Docker 守护进程不可访问"; failed=1
  fi

  if [[ -f "$ENV_FILE" ]]; then
    ok "$ENV_FILE 存在（权限 $(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE" 2>/dev/null || echo '?'))"
    local port
    port="$(env_value PORT 8080)"
    if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
      local owner
      owner="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | awk 'NR==2 {print $1}')"
      if [[ "$owner" == *docker* || "$owner" == *kook* ]]; then
        ok "端口 $port 已被本服务占用（正常）"
      else
        warn "端口 $port 已被其它进程（${owner}）占用，可能冲突：./deploy.sh up --port 8081"
        failed=1
      fi
    else
      ok "端口 $port 空闲"
    fi
    if [[ "$(env_value KOOK_DRYRUN 0)" != "1" && -z "$(env_value KOOK_TOKEN '')" ]]; then
      log "未在 .env 中配置 KOOK_TOKEN（可稍后在 WebUI 里填，属于正常流程）"
    fi
  else
    log "$ENV_FILE 尚未生成（首次 ./deploy.sh init 会自动创建）"
  fi

  local free_kb
  free_kb="$(df -Pk . | awk 'NR==2 {print $4}')"
  if [[ -n "$free_kb" ]] && (( free_kb < 512000 )); then
    warn "所在磁盘剩余空间不足 500MB（当前 $((free_kb / 1024))MB）"
    failed=1
  else
    ok "磁盘剩余空间 $(( ${free_kb:-0} / 1024 ))MB"
  fi

  command -v sqlite3 >/dev/null 2>&1 && ok "sqlite3 可用（备份可在线进行）" || log "未安装 sqlite3（备份会自动改为停服冷备，仍然安全）"
  command -v openssl >/dev/null 2>&1 && ok "openssl 可用（密钥随机生成）" || log "未安装 openssl（将回退到 /dev/urandom）"

  if (( failed == 0 )); then
    printf '\n'; ok "自检通过，可以执行 ./deploy.sh up"
  else
    printf '\n'; warn "自检发现上述问题，请先处理"
    return 1
  fi
}

# ---------------------------------------------------------------------------
# 交互菜单与参数解析
# ---------------------------------------------------------------------------

usage() {
  cat <<'EOF'
KOOK Ticket 部署脚本

用法: ./deploy.sh <命令> [选项]

命令:
  init                 生成 .env（随机 APP_SECRET 与初始管理员密码）
  up                   构建镜像并启动（首次自动 init）
  upgrade              备份 → 重建镜像 → 重启 → 健康检查
  restart              重启容器
  stop                 停止容器（保留数据）
  down                 停止并删除容器（保留数据）
  status               查看状态、健康、版本与访问地址
  logs                 跟踪日志
  backup               备份 data/ 到 backups/
  restore [备份文件]    从备份恢复（缺省使用最新备份）
  reset-password [用户名]  重置 WebUI 密码（缺省 admin）
  config               打印当前 .env（敏感值打码）
  doctor               环境自检（Docker、端口、磁盘）
  help                 显示本帮助

选项:
  --port N             宿主机端口（默认 8080）
  --bind ADDR          监听地址（默认 127.0.0.1，直接对外访问用 0.0.0.0）
  --tz TZ              业务时区（默认 Asia/Shanghai）
  --password PW        指定初始/新密码（默认随机生成）
  --dry-run            以 DryRun 演示模式部署（无需 KOOK Token 即可看界面）
  --force              init 时覆盖已存在的 .env
  --no-backup          upgrade 时跳过备份
  --yes                跳过交互确认

示例:
  ./deploy.sh init --port 8080
  ./deploy.sh up --bind 0.0.0.0
  ./deploy.sh upgrade
  ./deploy.sh backup
  ./deploy.sh reset-password admin
EOF
}

interactive_menu() {
  while true; do
    cat <<EOF

${C_BOLD}KOOK Ticket 部署菜单${C_RESET}
  1) 初始化并启动（自动生成 .env）
  2) 启动 / 重新构建
  3) 升级（备份 + 重建 + 健康检查）
  4) 查看状态
  5) 查看日志
  6) 备份数据
  7) 从备份恢复
  8) 重置 WebUI 密码
  9) 环境自检
  0) 退出
EOF
    read -r -p "请选择: " choice || exit 0
    case "$choice" in
      1) cmd_init; cmd_up ;;
      2) cmd_up ;;
      3) cmd_upgrade ;;
      4) cmd_status ;;
      5) cmd_logs ;;
      6) cmd_backup ;;
      7) cmd_restore "" ;;
      8) cmd_reset_password "" ;;
      9) cmd_doctor ;;
      0|"") exit 0 ;;
      *) warn "无效选择：$choice" ;;
    esac
  done
}

main() {
  local command="${1:-}"
  [[ $# -gt 0 ]] && shift || true

  OPT_PORT=""; OPT_BIND=""; OPT_TZ=""; OPT_PASSWORD=""; OPT_DRYRUN=""
  OPT_FORCE="0"; OPT_NOBACKUP="0"; ASSUME_YES="0"
  local positional=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --port) OPT_PORT="${2:-}"; shift 2 ;;
      --port=*) OPT_PORT="${1#*=}"; shift ;;
      --bind) OPT_BIND="${2:-}"; shift 2 ;;
      --bind=*) OPT_BIND="${1#*=}"; shift ;;
      --tz) OPT_TZ="${2:-}"; shift 2 ;;
      --tz=*) OPT_TZ="${1#*=}"; shift ;;
      --password) OPT_PASSWORD="${2:-}"; shift 2 ;;
      --password=*) OPT_PASSWORD="${1#*=}"; shift ;;
      --dry-run|--dryrun) OPT_DRYRUN="1"; shift ;;
      --force) OPT_FORCE="1"; shift ;;
      --no-backup) OPT_NOBACKUP="1"; shift ;;
      --yes|-y) ASSUME_YES="1"; shift ;;
      -h|--help) usage; exit 0 ;;
      --*) die "未知选项：$1（用 ./deploy.sh help 查看用法）" ;;
      -?*) die "未知选项：$1。长选项请用两个连字符，例如 --password（用 ./deploy.sh help 查看用法）" ;;
      *) positional+=("$1"); shift ;;
    esac
  done

  case "$command" in
    init) cmd_init ;;
    up|start) cmd_up ;;
    upgrade|update) cmd_upgrade ;;
    restart) cmd_restart ;;
    stop) cmd_stop ;;
    down) cmd_down ;;
    status|ps) cmd_status ;;
    logs|log) cmd_logs ;;
    backup) cmd_backup ;;
    restore) cmd_restore "${positional[0]:-}" ;;
    reset-password|reset) cmd_reset_password "${positional[0]:-}" ;;
    config|env) cmd_config ;;
    doctor|check) cmd_doctor ;;
    help|-h|--help) usage ;;
    "")
      if [[ -t 0 ]]; then interactive_menu; else usage; fi
      ;;
    *) usage; die "未知命令：$command" ;;
  esac
}

main "$@"
