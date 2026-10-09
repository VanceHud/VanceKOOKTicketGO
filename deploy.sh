#!/usr/bin/env bash
#
# KOOK Ticket 多实例部署脚本
#
# 一个实例 = 一个 KOOK 服务器：独立的 .env、数据目录、端口与容器，共享同一镜像。
#   实例配置：instances/<名字>/.env    数据：instances/<名字>/data    备份：backups/<名字>/
#
#   ./deploy.sh add <名字>          向导式新增实例（自动分配端口，Token/Guild/密码可留空）
#   ./deploy.sh list                列出全部实例（端口、状态、健康、版本）
#   ./deploy.sh up [实例]           构建并启动；不带实例名 = 全部实例
#   ./deploy.sh upgrade [实例]      备份 → 重建镜像 → 健康检查（不带名字 = 逐个滚动升级）
#   ./deploy.sh status [实例]       全部实例汇总 / 单实例详情
#   ./deploy.sh logs [实例]         跟踪日志（多实例时并行跟踪全部）
#   ./deploy.sh backup [实例]       备份到 backups/<实例>/（不带名字 = 全部）
#   ./deploy.sh restore <实例> [备份文件]
#   ./deploy.sh reset-password <实例> [用户名]
#   ./deploy.sh remove <实例>       移除容器（数据保留；--purge 连数据删除）
#   ./deploy.sh doctor              环境自检（Docker、实例端口冲突、磁盘）
#
# 旧版单实例部署（根目录 .env + data/）首次运行任意命令时自动迁移为 instances/default。
# 不带参数运行会进入交互菜单。所有命令都可通过 --help 查看选项。
#
set -Eeuo pipefail
# 数据备份包含 APP_SECRET 和数据库，默认权限不能跟随调用者的宽松 umask。
umask 077

# ---------------------------------------------------------------------------
# 基础变量与输出
# ---------------------------------------------------------------------------

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

INSTANCES_DIR="instances"
SERVICE="kook-ticket"
KEEP_BACKUPS=10
# 自动分配端口的起点（从 9235 起找第一个空闲端口）
BASE_PORT=9235
# 旧版单实例部署的痕迹（迁移用）
LEGACY_ENV=".env"
LEGACY_DATA="data"
LEGACY_CONTAINER="kook-ticket"

# 当前实例上下文（由 load_instance 设置；compose()/env_value() 等依赖它们）。
# 空值表示尚未选中实例，此时只能调用 compose_raw()。
ENV_FILE=""
DATA_DIR=""
CONTAINER=""
PROJECT=""
BACKUP_DIR=""
CURRENT_INSTANCE=""

# 命令行选项（main 解析前先给默认值，交互菜单直接调用 cmd_* 时也能安全引用）
OPT_PORT=""; OPT_BIND=""; OPT_TZ=""; OPT_PASSWORD=""; OPT_DRYRUN=""
OPT_TOKEN=""; OPT_GUILD=""; OPT_INSTANCE=""
OPT_FORCE="0"; OPT_NOBACKUP="0"; OPT_PURGE="0"; ASSUME_YES="0"

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

# compose_raw 不带实例上下文直接调用 compose（迁移旧项目、doctor 等）
compose_raw() { ${COMPOSE_BIN} "$@"; }
# compose 在当前实例的 compose 项目上操作（需先 load_instance）
compose() {
  [[ -n "$PROJECT" ]] || die "内部错误：compose() 在未加载实例时被调用"
  ${COMPOSE_BIN} -p "$PROJECT" --env-file "$ENV_FILE" "$@"
}
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

# ---------------------------------------------------------------------------
# 实例管理
# ---------------------------------------------------------------------------

# valid_name 校验实例名：它会被拼进目录、容器名与 compose 项目名，必须收敛字符集。
valid_name() {
  local re='^[a-z0-9][a-z0-9_-]{0,29}$'
  [[ "$1" =~ $re ]]
}

# list_instances 返回空格分隔的实例名（以 instances/<名字>/.env 为准）。
list_instances() {
  local out="" d
  for d in "$INSTANCES_DIR"/*/; do
    d="${d%/}"
    if [[ -f "$d/.env" ]]; then
      out="$out ${d##*/}"
    fi
  done
  printf '%s' "${out# }"
}

instance_count() {
  local n count=0
  for n in $(list_instances); do count=$((count + 1)); done
  printf '%s' "$count"
}

# load_instance 载入实例上下文：后续 env_value/compose/wait_ready 都作用于该实例。
load_instance() {
  local name="$1"
  valid_name "$name" || die "实例名只能包含小写字母、数字、-、_（1-30 位）：$name"
  [[ -f "$INSTANCES_DIR/$name/.env" ]] || die "实例不存在：${name}（查看全部：./deploy.sh list）"

  CURRENT_INSTANCE="$name"
  ENV_FILE="$INSTANCES_DIR/$name/.env"
  DATA_DIR="$INSTANCES_DIR/$name/data"
  BACKUP_DIR="backups/$name"
  CONTAINER="$(env_value CONTAINER_NAME "kook-ticket-$name")"
  PROJECT="kook-ticket-$name"
}

# instance_ports 返回已被各实例占用的端口列表。
instance_ports() {
  local out="" n f
  for n in $(list_instances); do
    f="$INSTANCES_DIR/$n/.env"
    if [[ -f "$f" ]]; then
      out="$out $(grep -E '^PORT=' "$f" 2>/dev/null | tail -1 | cut -d= -f2-)"
    fi
  done
  printf '%s' "${out# }"
}

# port_listening 判断端口是否被监听（没有 lsof 时无法检测，按空闲处理；
# 真冲突会在容器启动时报端口绑定错误，wait_ready 会给出日志）。
port_listening() {
  command -v lsof >/dev/null 2>&1 || return 1
  lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}

# free_port 从 BASE_PORT 起找第一个空闲端口（避开其它实例已用的与正在监听的）。
free_port() {
  local used p tries=0
  used="$(instance_ports)"
  p="$BASE_PORT"
  while (( tries < 1000 )); do
    if [[ " $used " == *" $p "* ]] || port_listening "$p"; then
      p=$((p + 1))
      tries=$((tries + 1))
      continue
    fi
    printf '%s' "$p"
    return 0
  done
  die "从 ${BASE_PORT} 起连续 1000 个端口都不可用，请用 --port 指定"
}

# ---------------------------------------------------------------------------
# 旧版单实例部署迁移
# ---------------------------------------------------------------------------

# maybe_migrate 把根目录 .env + data/ 迁移为 instances/default（幂等）。
#
# 迁移必须先停旧容器再移动文件：bind mount 绑定的是目录 inode，
# 若旧容器仍在运行，新旧两个容器会同时读写同一个数据库。
maybe_migrate() {
  [[ -f "$LEGACY_ENV" || -d "$LEGACY_DATA" ]] || return 0

  if [[ -n "$(list_instances)" ]]; then
    warn "根目录存在旧版 .env/data，但 instances/ 已有实例；忽略旧文件（确认无用后可删除）"
    return 0
  fi
  if [[ ! -f "$LEGACY_ENV" ]]; then
    warn "根目录有 data/ 但没有 .env，无法自动迁移；如需保留数据请手动移到 instances/default/data 后执行 ./deploy.sh add default"
    return 0
  fi
  if ! docker_cli info >/dev/null 2>&1; then
    warn "Docker 不可用，暂不迁移旧版部署；启动 Docker 后重试本命令即可"
    return 0
  fi

  step "检测到旧版单实例部署，自动迁移为 instances/default"

  # 停掉旧容器（旧版容器名固定 kook-ticket）。
  # 优先按容器身上的 compose 项目标签 down，标签缺失时直接删容器。
  if docker_cli inspect "$LEGACY_CONTAINER" >/dev/null 2>&1; then
    local old_project=""
    old_project="$(docker_cli inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$LEGACY_CONTAINER" 2>/dev/null || true)"
    if [[ -n "$old_project" && "$old_project" != "kook-ticket-default" ]]; then
      log "停止旧容器（compose 项目：${old_project}）"
      compose_raw -p "$old_project" down --remove-orphans || docker_cli rm -f "$LEGACY_CONTAINER"
    else
      docker_cli rm -f "$LEGACY_CONTAINER"
    fi
  fi

  mkdir -p "$INSTANCES_DIR/default"
  mv "$LEGACY_ENV" "$INSTANCES_DIR/default/.env"
  ENV_FILE="$INSTANCES_DIR/default/.env"
  # 注入多实例需要的两个插值键；容器名沿用旧值 kook-ticket，运维习惯不变
  set_env_value CONTAINER_NAME "kook-ticket"
  set_env_value DATA_DIR_HOST "./instances/default/data"
  if [[ -d "$LEGACY_DATA" ]]; then
    mv "$LEGACY_DATA" "$INSTANCES_DIR/default/data"
  fi

  # 旧版备份是平铺的 kook-ticket-*.tar.gz，归入 default 实例名下
  if ls backups/*.tar.gz >/dev/null 2>&1; then
    mkdir -p "backups/default"
    mv backups/*.tar.gz "backups/default/"
  fi

  ENV_FILE=""
  ok "迁移完成：.env 与 data/ 已移入 instances/default/（数据未改动）"
  log "容器已停止，执行 ./deploy.sh up default 用新布局启动"
}

# ---------------------------------------------------------------------------
# .env 生成（新增实例）
# ---------------------------------------------------------------------------

# write_instance_env 写出实例的 .env（权限 600）。
write_instance_env() {
  local path="$1" name="$2" port="$3" bind="$4" tz="$5" dry="$6" password="$7" token="$8" guild="$9"
  local container data_host puid pgid app_secret tmp

  # default 实例沿用旧版容器名 kook-ticket，方便复用既有运维命令与文档
  if [[ "$name" == "default" ]]; then
    container="kook-ticket"
  else
    container="kook-ticket-$name"
  fi
  data_host="./instances/$name/data"
  puid="$(id -u)"
  pgid="$(id -g)"
  app_secret="$(rand_hex 32)"

  tmp="$(mktemp)"
  {
    printf '# 由 deploy.sh 生成于 %s（实例：%s）\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$name"
    printf '# 该文件含密钥，请勿提交到仓库；修改后执行 ./deploy.sh up %s 生效。\n' "$name"
    printf '\n# ---- 基础 ----\n'
    printf 'PORT=%s\n' "$port"
    printf 'BIND_ADDR=%s\n' "$bind"
    printf 'TICKET_TZ=%s\n' "$tz"
    printf 'LOG_LEVEL=info\n'
    printf '\n# ---- 多实例标识（compose 插值用，勿改）----\n'
    printf 'CONTAINER_NAME=%s\n' "$container"
    printf 'DATA_DIR_HOST=%s\n' "$data_host"
    printf '\n# ---- 容器运行身份（Linux 上需与数据目录属主一致）----\n'
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
    printf 'ADMIN_USERNAME=admin\n'
    printf 'ADMIN_PASSWORD=%s\n' "$password"
    printf '\n# ---- KOOK 接入（也可留空，稍后在 WebUI 里填）----\n'
    printf 'KOOK_DRYRUN=%s\n' "$dry"
    printf 'KOOK_TOKEN=%s\n' "$token"
    printf 'KOOK_GUILD_ID=%s\n' "$guild"
    printf '\n# ---- 会话与登录保护 ----\n'
    printf 'SESSION_IDLE_HOURS=12\n'
    printf 'SESSION_MAX_DAYS=7\n'
    printf 'LOGIN_MAX_FAILS=5\n'
    printf 'LOGIN_WINDOW_MINUTES=15\n'
    printf 'LOGIN_LOCK_MINUTES=15\n'
  } > "$tmp"
  mv "$tmp" "$path"
  chmod 600 "$path"
}

# prompt_secret 交互式录入敏感值（不回显），输出到 stdout、提示走 stderr。
# 回车 = 跳过（输出空串）；录入后展示打码值并要求确认。
prompt_secret() {
  local label="$1" hint="$2" value sure
  while true; do
    read -r -s -p "${label}（${hint}）: " value || return 1
    echo >&2
    if [[ -z "$value" ]]; then
      return 0
    fi
    printf '  已记录 %s\n' "$(mask "$value")" >&2
    read -r -p "  确认使用？[Y/n] " sure || return 1
    if [[ ! "$sure" =~ ^[Nn]$ ]]; then
      printf '%s' "$value"
      return 0
    fi
  done
}

# prompt_password 录入初始管理员密码（回车 = 随机生成）。
prompt_password() {
  local value
  read -r -s -p "WebUI 管理员密码（回车 = 随机生成并打印在日志，首登强制改密）: " value || return 1
  echo >&2
  printf '%s' "$value"
}

# prepare_data_dir 创建并修正实例数据目录权限（Linux 上对齐容器运行身份）。
prepare_data_dir() {
  mkdir -p "$DATA_DIR"
  chmod 700 "$DATA_DIR" 2>/dev/null || true
  if [[ "$(uname -s)" == "Linux" ]]; then
    local puid pgid
    puid="$(env_value PUID "$(id -u)")"; pgid="$(env_value PGID "$(id -g)")"
    if [[ "$(stat -c '%u:%g' "$DATA_DIR" 2>/dev/null || echo '')" != "$puid:$pgid" ]]; then
      if [[ "$(id -u)" == "0" ]]; then
        chown -R "$puid:$pgid" "$DATA_DIR"
      else
        warn "数据目录属主与容器运行身份（$puid:${pgid}）不一致，若容器反复重启请执行：sudo chown -R $puid:$pgid $DATA_DIR"
      fi
    fi
  fi
}

# ---------------------------------------------------------------------------
# 新增实例（向导）
# ---------------------------------------------------------------------------

cmd_add() {
  local name="${1:-}"

  if [[ -z "$name" ]]; then
    if [[ -t 0 ]]; then
      read -r -p "实例名（小写字母/数字/-/_，将作为目录与容器名，如 myguild）: " name || die "已取消"
    else
      die "add 需要实例名：./deploy.sh add <名字>"
    fi
  fi
  valid_name "$name" || die "实例名只能包含小写字母、数字、-、_，以字母或数字开头（1-30 位）：$name"

  if [[ -f "$INSTANCES_DIR/$name/.env" ]]; then
    if [[ "$OPT_FORCE" == "1" ]]; then
      confirm "重新生成实例 $name 的 .env？（数据保留；新 APP_SECRET 会使已存的 KOOK Token 需重新填写）" || die "已取消"
      rm -f "$INSTANCES_DIR/$name/.env"
    else
      die "实例已存在：${name}（启动：./deploy.sh up ${name}；重建配置可加 --force）"
    fi
  elif [[ -d "$INSTANCES_DIR/$name/data" ]]; then
    # remove 保留的数据目录：允许复用（例如 remove 后反悔、或误删 .env 重建）
    warn "检测到 ${INSTANCES_DIR}/${name}/data 已存在，将复用其中的数据"
    log "注意：新 .env 会生成新的 APP_SECRET，已存的 KOOK Token 需在 WebUI 重新填写；数据库账号保持不变"
  fi

  local port bind tz dry password token guild
  bind="${OPT_BIND:-127.0.0.1}"
  tz="${OPT_TZ:-Asia/Shanghai}"
  dry="${OPT_DRYRUN:-0}"
  password="${OPT_PASSWORD:-}"
  token="${OPT_TOKEN:-}"
  guild="${OPT_GUILD:-}"

  if [[ -n "$OPT_PORT" ]]; then
    port="$OPT_PORT"
    if port_listening "$port"; then
      warn "端口 $port 当前已被监听，容器可能起不来"
    fi
  else
    port="$(free_port)"
    local others
    others="$(instance_ports)"
    if [[ -z "$others" ]]; then
      log "端口：$port"
    else
      log "自动分配端口 ${port}（已避开：${others}）"
    fi
  fi

  # 向导：终端下依次询问缺失项；全部可通过 --token/--guild/--password 预填跳过
  if [[ -t 0 ]]; then
    if [[ -z "$token" ]]; then
      token="$(prompt_secret "KOOK Bot Token" "回车跳过，稍后在 WebUI 填写" || true)"
    fi
    if [[ -z "$guild" ]]; then
      read -r -p "KOOK 服务器(Guild) ID（回车跳过，稍后在 WebUI 填写）: " guild || guild=""
    fi
    if [[ -z "$password" ]]; then
      password="$(prompt_password || true)"
    fi
  fi

  step "创建实例 ${name}（端口 ${port}，数据目录 instances/${name}/data）"
  mkdir -p "$INSTANCES_DIR/$name"
  write_instance_env "$INSTANCES_DIR/$name/.env" "$name" "$port" "$bind" "$tz" "$dry" "$password" "$token" "$guild"
  ok "已生成 $INSTANCES_DIR/$name/.env（权限 600）"
  if [[ -z "$password" ]]; then
    log "初始管理员密码：随机生成，打印在启动日志中，首登强制改密"
  else
    log "初始管理员密码：已按输入设置（不会强制改密）"
  fi

  load_instance "$name"
  prepare_data_dir

  step "构建镜像并启动实例 ${name}"
  compose up -d --build --remove-orphans
  wait_ready
  cmd_status_one
  show_initial_password
  ok "实例 ${name} 已就绪"
}

# cmd_init 兼容旧命令：等价于 add（缺省实例名 default）。
cmd_init() {
  local name="${1:-default}"
  if [[ -f "$INSTANCES_DIR/$name/.env" && "$OPT_FORCE" != "1" ]]; then
    warn "实例 $name 已存在（.env 不变）。启动：./deploy.sh up $name"
    return 0
  fi
  cmd_add "$name"
}

# ---------------------------------------------------------------------------
# 构建与启动
# ---------------------------------------------------------------------------

cmd_up_one() {
  if [[ -n "$OPT_PORT" ]]; then set_env_value PORT "$OPT_PORT"; log "已更新端口为 $OPT_PORT"; fi
  if [[ -n "$OPT_BIND" ]]; then set_env_value BIND_ADDR "$OPT_BIND"; log "已更新监听地址为 $OPT_BIND"; fi
  if [[ -n "$OPT_TZ" ]]; then set_env_value TICKET_TZ "$OPT_TZ"; log "已更新业务时区为 $OPT_TZ"; fi
  if [[ -n "$OPT_DRYRUN" ]]; then set_env_value KOOK_DRYRUN "$OPT_DRYRUN"; fi

  # 手工创建的 .env 可能缺多实例插值键，补默认值
  if ! grep -q '^DATA_DIR_HOST=' "$ENV_FILE"; then
    set_env_value DATA_DIR_HOST "./instances/$CURRENT_INSTANCE/data"
  fi
  if ! grep -q '^CONTAINER_NAME=' "$ENV_FILE"; then
    set_env_value CONTAINER_NAME "kook-ticket-$CURRENT_INSTANCE"
  fi

  prepare_data_dir
  compose up -d --build --remove-orphans
  wait_ready
}

cmd_up() {
  local target="$1"

  if [[ -z "$(list_instances)" && -z "$target" ]]; then
    log "还没有任何实例，开始创建第一个（名称 default）"
    cmd_add "default"
    return
  fi

  if [[ -n "$target" ]]; then
    load_instance "$target"
    step "启动实例 $target"
    cmd_up_one
    cmd_status_one
    show_initial_password
    return
  fi

  if [[ -n "$OPT_PORT" || -n "$OPT_BIND" || -n "$OPT_TZ" ]]; then
    die "--port/--bind/--tz 只对单个实例生效：./deploy.sh up <实例名> --port 8081"
  fi

  local n
  for n in $(list_instances); do
    step "启动实例 $n"
    load_instance "$n"
    cmd_up_one
    show_initial_password
  done
  cmd_status_table
}

restart_one() {
  compose restart
  wait_ready
}

stop_one() {
  compose stop
}

down_one() {
  compose down --remove-orphans
}

cmd_restart() {
  local target="$1" n
  if [[ -n "$target" ]]; then
    load_instance "$target"
    step "重启实例 $target"
    restart_one
    return
  fi
  for n in $(list_instances); do
    step "重启实例 $n"
    load_instance "$n"
    restart_one
  done
}

cmd_stop() {
  local target="$1" n
  if [[ -n "$target" ]]; then
    load_instance "$target"
    step "停止实例 ${target}（数据保留在 ${DATA_DIR}）"
    stop_one
    ok "已停止"
    return
  fi
  for n in $(list_instances); do
    step "停止实例 ${n}（数据保留在 ${DATA_DIR}）"
    load_instance "$n"
    stop_one
  done
  ok "全部实例已停止"
}

cmd_down() {
  local target="$1" n
  if [[ -n "$target" ]]; then
    load_instance "$target"
    step "停止并删除实例 $target 的容器（数据保留在 ${DATA_DIR}）"
    down_one
    ok "已删除容器；下次 ./deploy.sh up $target 会用同一份数据重新创建"
    return
  fi
  for n in $(list_instances); do
    step "停止并删除实例 $n 的容器（数据保留）"
    load_instance "$n"
    down_one
  done
  ok "全部容器已删除；下次 ./deploy.sh up 会用同一份数据重新创建"
}

wait_ready() {
  local timeout="${1:-120}" elapsed=0 url status
  url="http://127.0.0.1:$(env_value PORT 9235)/healthz"

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
  die "实例 ${CURRENT_INSTANCE} 启动失败，请按上面的日志排查（部署教程：docs/DEPLOYMENT.md 第 9 节排错速查）"
}

# ---------------------------------------------------------------------------
# 升级
# ---------------------------------------------------------------------------

# git_pull_latest 拉取最新代码（工作区干净时）；失败不阻塞升级。
git_pull_latest() {
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
}

upgrade_one() {
  local before="" after=""
  before="$(docker_cli exec "$CONTAINER" /app/kook-ticket -version 2>/dev/null || true)"
  if [[ -n "$before" ]]; then
    log "当前版本：$before"
  else
    log "当前版本：未知（容器可能未在运行）"
  fi

  if [[ "$OPT_NOBACKUP" != "1" ]]; then
    cmd_backup_one
  else
    warn "已按 --no-backup 跳过备份"
  fi

  compose up -d --build --remove-orphans
  wait_ready

  after="$(docker_cli exec "$CONTAINER" /app/kook-ticket -version 2>/dev/null || true)"
  ok "实例 ${CURRENT_INSTANCE} 升级完成：${before:-未知} → ${after:-未知}"
}

cmd_upgrade() {
  local target="$1"
  local names
  names="$(list_instances)"
  [[ -n "$names" ]] || die "还没有任何实例（先 ./deploy.sh add <名字>）"

  git_pull_latest

  if [[ -n "$target" ]]; then
    load_instance "$target"
    step "升级实例 $target"
    upgrade_one
    log "数据库结构由程序启动时自动迁移；如新版本异常，可用 ./deploy.sh restore $target $(latest_backup 2>/dev/null || echo '<备份文件>') 回滚数据"
    return
  fi

  local n
  for n in $names; do
    step "升级实例 $n"
    load_instance "$n"
    upgrade_one
  done
  ok "全部实例升级完成"
  log "如某个实例异常，可用 ./deploy.sh restore <实例名> <备份文件> 单独回滚"
}

# ---------------------------------------------------------------------------
# 状态与日志
# ---------------------------------------------------------------------------

cmd_status_table() {
  printf '\n%-16s %-6s %-10s %-9s %-12s %s\n' NAME PORT STATE HEALTH VERSION DATA
  local n state health version size port
  for n in $(list_instances); do
    load_instance "$n"
    port="$(env_value PORT 9235)"
    state="$(docker_cli inspect -f '{{.State.Status}}' "$CONTAINER" 2>/dev/null || echo missing)"
    health="$(curl -fsS --max-time 3 "http://127.0.0.1:${port}/healthz" 2>/dev/null | head -c 40 || true)"
    if [[ -z "$health" ]]; then
      health="—"
    fi
    if [[ "$state" == "running" ]]; then
      version="$(docker_cli exec "$CONTAINER" /app/kook-ticket -version 2>/dev/null || echo '?')"
    else
      version="—"
    fi
    size="$(du -sh "$DATA_DIR" 2>/dev/null | cut -f1 || echo '—')"
    printf '%-16s %-6s %-10s %-9s %-12s %s\n' "$n" "$port" "$state" "$health" "$version" "$size"
  done
}

cmd_status_one() {
  step "实例 ${CURRENT_INSTANCE} 状态"
  compose ps || true

  local port bind version health
  port="$(env_value PORT 9235)"
  bind="$(env_value BIND_ADDR 127.0.0.1)"
  version="$(docker_cli exec "$CONTAINER" /app/kook-ticket -version 2>/dev/null || true)"
  health="$(curl -fsS --max-time 5 "http://127.0.0.1:${port}/healthz" 2>/dev/null || true)"

  printf '\n%s访问地址%s  http://%s:%s\n' "$C_BOLD" "$C_RESET" "$([[ "$bind" == "0.0.0.0" ]] && echo '<服务器IP>' || echo '127.0.0.1')" "$port"
  printf '%s运行版本%s  %s\n' "$C_BOLD" "$C_RESET" "${version:-未知}"
  printf '%s健康检查%s  %s\n' "$C_BOLD" "$C_RESET" "${health:-未就绪}"
  printf '%s业务时区%s  %s\n' "$C_BOLD" "$C_RESET" "$(env_value TICKET_TZ Asia/Shanghai)"
  printf '%s数据目录%s  %s（大小 %s）\n' "$C_BOLD" "$C_RESET" "$DATA_DIR" "$(du -sh "$DATA_DIR" 2>/dev/null | cut -f1 || echo '—')"
  printf '%s演示模式%s  %s\n' "$C_BOLD" "$C_RESET" "$([[ "$(env_value KOOK_DRYRUN 0)" == "1" ]] && echo '开启（不连接 KOOK）' || echo '关闭')"

  if [[ -z "$health" ]]; then
    printf '\n'
    warn "健康检查未通过，最近的日志："
    compose logs --tail=20 "$SERVICE" || true
  fi
}

cmd_status() {
  local target="$1"
  if [[ -n "$target" ]]; then
    load_instance "$target"
    cmd_status_one
    return
  fi
  step "全部实例状态"
  cmd_status_table
  printf '\n'
  log "单实例详情：./deploy.sh status <实例名>；跟踪日志：./deploy.sh logs [实例名]"
}

cmd_list() {
  local names
  names="$(list_instances)"
  if [[ -z "$names" ]]; then
    log "还没有任何实例；创建第一个：./deploy.sh add <名字>"
    return 0
  fi
  step "实例列表（共 $(instance_count) 个）"
  cmd_status_table
  printf '\n'
  log "操作单个实例：./deploy.sh <命令> <实例名>（如 status/logs/backup/upgrade）"
}

cmd_logs() {
  local target="$1"
  local names
  names="$(list_instances)"
  [[ -n "$names" ]] || die "还没有任何实例"

  if [[ -n "$target" ]]; then
    load_instance "$target"
    log "跟踪实例 $target 日志（Ctrl+C 退出）"
    compose logs -f --tail=200 "$SERVICE"
    return
  fi

  # 只有一个实例时直接跟踪；多个实例时并行跟踪全部（docker logs 按容器名前缀区分）
  if [[ "$(instance_count)" -eq 1 ]]; then
    load_instance "$names"
    log "跟踪实例 $names 日志（Ctrl+C 退出）"
    compose logs -f --tail=200 "$SERVICE"
    return
  fi

  log "并行跟踪 $(instance_count) 个实例的日志（Ctrl+C 退出）"
  local n pids=""
  for n in $names; do
    load_instance "$n"
    docker_cli logs -f --tail=50 "$CONTAINER" &
    pids="$pids $!"
  done
  trap 'kill $pids 2>/dev/null || true; exit 130' INT
  wait || true
  trap - INT
}

# show_initial_password 从启动日志里取出首次生成的初始密码并提示。
#
# 说明：密码只在首次初始化时打印一次，取不到就说明数据库里已有账号。
show_initial_password() {
  [[ -z "$(env_value ADMIN_PASSWORD '')" ]] || return 0
  local line
  line="$(compose logs --tail=300 "$SERVICE" 2>/dev/null | grep -o 'initial_password=[^ "]*' | tail -1 || true)"
  [[ -n "$line" ]] || return 0
  printf '\n%s实例 %s 的初始管理员密码%s\n  用户名：%s\n  密  码：%s\n\n' \
    "$C_BOLD" "$CURRENT_INSTANCE" "$C_RESET" "$(env_value ADMIN_USERNAME admin)" "${line#initial_password=}"
  warn "该密码只打印一次，请在首次登录后立即改密（系统会强制要求）"
}

# ---------------------------------------------------------------------------
# 备份与恢复
# ---------------------------------------------------------------------------

latest_backup() {
  ls -1t "$BACKUP_DIR"/*.tar.gz 2>/dev/null | head -1
}

cmd_backup() {
  local target="$1" n
  if [[ -n "$target" ]]; then
    load_instance "$target"
    cmd_backup_one
    return
  fi
  local names
  names="$(list_instances)"
  [[ -n "$names" ]] || die "还没有任何实例"
  for n in $names; do
    step "备份实例 $n"
    load_instance "$n"
    cmd_backup_one
  done
  ok "全部实例备份完成（各自保存在 backups/<实例名>/）"
}

cmd_backup_one() {
  mkdir -p "$BACKUP_DIR"

  local stamp file stage online=0 suffix=0
  stamp="$(date '+%Y%m%d-%H%M%S')"
  file="$BACKUP_DIR/kook-ticket-${CURRENT_INSTANCE}-$stamp.tar.gz"
  # 同一秒内可能触发多次备份（例如 upgrade 又会备份一次）：
  # 时间戳精度不够会互相覆盖，这里追加序号避免丢失快照。
  while [[ -e "$file" ]]; do
    suffix=$((suffix + 1))
    file="$BACKUP_DIR/kook-ticket-${CURRENT_INSTANCE}-$stamp-$suffix.tar.gz"
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
    local f
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
  count="$(ls -1t "$BACKUP_DIR"/*.tar.gz 2>/dev/null | wc -l | tr -d ' ')"
  if (( count > KEEP_BACKUPS )); then
    ls -1t "$BACKUP_DIR"/*.tar.gz | tail -n +$((KEEP_BACKUPS + 1)) | while read -r old; do
      rm -f "$old"
      log "已清理旧备份：$old"
    done
  fi
}

cmd_restore() {
  local inst="${1:-}" file="${2:-}"

  if [[ -z "$inst" ]]; then
    if [[ "$(instance_count)" -eq 1 ]]; then
      inst="$(list_instances)"
      log "只有一个实例，恢复目标：$inst"
    else
      die "请指定实例：./deploy.sh restore <实例名> [备份文件]（现有实例：$(list_instances)）"
    fi
  fi
  load_instance "$inst"

  if [[ -z "$file" ]]; then
    file="$(latest_backup || true)"
    [[ -n "$file" ]] || die "未指定备份文件，且 $BACKUP_DIR/ 下没有可用备份"
    log "未指定文件，使用最新备份：$file"
  fi
  [[ -f "$file" ]] || die "备份文件不存在：$file"

  confirm "恢复会用备份覆盖实例 ${inst} 的当前数据（建议先执行 ./deploy.sh backup ${inst}），继续？" || die "已取消"
  step "恢复实例 ${inst} 的数据"

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
    log "如需改用备份中的密钥：cp ${aside}/deploy-env $ENV_FILE 然后 ./deploy.sh up $inst"
  fi
  rm -rf "$stage"

  chmod 700 "$DATA_DIR" 2>/dev/null || true
  find "$DATA_DIR" -maxdepth 1 -type f -exec chmod 600 {} \; 2>/dev/null || true

  compose start "$SERVICE" >/dev/null 2>&1 || compose up -d --remove-orphans
  wait_ready

  ok "实例 ${inst} 恢复完成（还原 ${restored} 个文件）"
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
  local inst="${1:-}" user="${2:-}"

  if [[ -z "$inst" ]]; then
    if [[ "$(instance_count)" -eq 1 ]]; then
      inst="$(list_instances)"
    else
      die "请指定实例：./deploy.sh reset-password <实例名> [用户名]（现有实例：$(list_instances)）"
    fi
  fi
  load_instance "$inst"
  user="${user:-$(env_value ADMIN_USERNAME admin)}"

  local password="${OPT_PASSWORD:-}"
  local was_running=0

  if docker_cli ps --filter "name=^/${CONTAINER}$" --filter "status=running" -q | grep -q .; then
    was_running=1
  fi

  step "重置实例 ${inst} 账号 ${user} 的密码"
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

config_one() {
  step "实例 ${CURRENT_INSTANCE} 的 .env（敏感值已打码）"
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
}

cmd_config() {
  local target="$1" n
  if [[ -n "$target" ]]; then
    load_instance "$target"
    config_one
    printf '\n（如需查看明文：cat %s，请勿外传）\n' "$ENV_FILE"
    return
  fi
  local names
  names="$(list_instances)"
  [[ -n "$names" ]] || die "还没有任何实例"
  for n in $names; do
    load_instance "$n"
    config_one
  done
  printf '\n（如需查看明文：cat instances/<实例名>/.env，请勿外传）\n'
}

cmd_remove() {
  local name="${1:-}"
  if [[ -z "$name" ]]; then
    die "用法：./deploy.sh remove <实例名> [--purge]"
  fi
  load_instance "$name"

  confirm "停止并移除实例 ${name}？容器与 compose 项目删除，数据保留在 ${INSTANCES_DIR}/${name}/" || die "已取消"
  step "移除实例 ${name}"
  compose down --remove-orphans

  if [[ "$OPT_PURGE" == "1" ]]; then
    confirm "连数据、配置与备份一起删除？（${INSTANCES_DIR}/${name}/ 与 ${BACKUP_DIR}/，不可恢复）" || die "已取消（容器已移除，数据保留）"
    rm -rf "${INSTANCES_DIR:?}/${name:?}" "${BACKUP_DIR:?}"
    ok "实例 ${name} 及其全部数据已删除"
  else
    # .env 改名保留：实例从 list/up/backup 等命令中消失，但配置可完整找回
    if [[ -f "$ENV_FILE" ]]; then
      mv "$ENV_FILE" "$INSTANCES_DIR/$name/.env.removed"
    fi
    ok "实例 ${name} 已移除；数据保留在 ${INSTANCES_DIR}/${name}/data"
    log "重新启用：./deploy.sh add ${name}（复用数据，需重填 Token）；或 mv ${INSTANCES_DIR}/${name}/.env.removed ${INSTANCES_DIR}/${name}/.env 后 ./deploy.sh up ${name}"
  fi
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

  # 实例检查：端口冲突（实例之间 / 与其它进程）、数据目录、Token 配置提示
  local names
  names="$(list_instances)"
  if [[ -z "$names" ]]; then
    log "还没有实例（./deploy.sh add <名字> 创建）"
  else
    local seen_ports="" n port owner
    for n in $names; do
      load_instance "$n"
      port="$(env_value PORT 9235)"
      if [[ " $seen_ports " == *" $port "* ]]; then
        warn "端口 $port 被多个实例使用（含 ${n}），后启动的会绑定失败"
        failed=1
      fi
      seen_ports="$seen_ports $port"

      if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
        owner="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | awk 'NR==2 {print $1}')"
        if [[ "$owner" == *docker* || "$owner" == *com.docker* || "$owner" == *kook* ]]; then
          ok "实例 ${n}：端口 $port 由本服务占用（正常）"
        else
          warn "实例 ${n}：端口 $port 已被其它进程（${owner}）占用，可能冲突"
          failed=1
        fi
      else
        ok "实例 ${n}：端口 $port 空闲（容器未运行）"
      fi

      if [[ -d "$DATA_DIR" ]]; then
        ok "实例 ${n}：数据目录存在"
      else
        warn "实例 ${n}：数据目录 $DATA_DIR 不存在（up 时会自动创建）"
      fi

      if [[ "$(env_value KOOK_DRYRUN 0)" != "1" && -z "$(env_value KOOK_TOKEN '')" ]]; then
        log "实例 ${n}：未在 .env 中配置 KOOK_TOKEN（可稍后在 WebUI 里填，属于正常流程）"
      fi
    done
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
    printf '\n'; ok "自检通过，可以执行 ./deploy.sh add <名字> 或 ./deploy.sh up"
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
KOOK Ticket 多实例部署脚本

一个实例 = 一个 KOOK 服务器：独立的 .env、数据目录、端口与容器，共享同一镜像。
实例配置在 instances/<名字>/.env，数据在 instances/<名字>/data，备份在 backups/<名字>/。

用法: ./deploy.sh <命令> [实例名] [选项]

命令:
  add <名字>              向导式新增实例（自动分配端口；Token/Guild/密码可留空）
  init [名字]              兼容旧命令：等价于 add（缺省实例名 default）
  list                     列出全部实例（端口、容器状态、健康、版本、数据大小）
  up [实例]                构建并启动；缺省 = 全部实例（无实例时自动进入新增向导）
  upgrade [实例]           备份 → 重建镜像 → 健康检查；缺省 = 逐个滚动升级全部
  restart [实例]           重启；缺省 = 全部
  stop [实例]              停止（保留数据）；缺省 = 全部
  down [实例]              停止并删除容器（保留数据）；缺省 = 全部
  status [实例]            全部实例汇总 / 单实例详情
  logs [实例]              跟踪日志；缺省 = 并行跟踪全部实例
  backup [实例]            备份到 backups/<实例>/；缺省 = 全部
  restore <实例> [备份文件] 恢复（缺省使用该实例的最新备份）
  reset-password <实例> [用户名]  重置 WebUI 密码
  remove <实例>            移除实例（容器删除，数据保留在原目录；--purge 连数据与备份删除）
  config [实例]            打印 .env（敏感值打码）；缺省 = 全部
  doctor                   环境自检（Docker、实例端口冲突、磁盘）
  migrate                  手动触发旧版部署迁移（通常首次运行任意命令时自动完成）
  help                     显示本帮助

选项:
  --port N                 指定端口（add 时缺省自动分配；up 时需配合实例名）
  --bind ADDR              监听地址（默认 127.0.0.1，直接对外访问用 0.0.0.0）
  --tz TZ                  业务时区（默认 Asia/Shanghai）
  --token TOKEN            add 时直接提供 KOOK Token（跳过向导询问）
  --guild ID               add 时直接提供 KOOK 服务器(Guild) ID
  --password PW            指定初始/新密码（默认随机生成）
  --instance NAME          等价于把实例名作为第一个位置参数
  --dry-run                以 DryRun 演示模式部署（无需 KOOK Token 即可看界面）
  --force                  add/init 时覆盖已存在的 .env（数据保留）
  --purge                  remove 时连数据、配置与备份一起删除
  --no-backup              upgrade 时跳过备份
  --yes                    跳过交互确认

示例:
  ./deploy.sh add myguild                  # 向导式新增并启动（自动分配端口）
  ./deploy.sh add beta --dry-run --port 9250
  ./deploy.sh list
  ./deploy.sh status myguild
  ./deploy.sh up                           # 启动全部实例
  ./deploy.sh upgrade                      # 全部实例滚动升级（逐个备份+重建）
  ./deploy.sh backup myguild
  ./deploy.sh restore myguild
  ./deploy.sh reset-password myguild admin
  ./deploy.sh remove myguild               # 移除容器，数据保留
  ./deploy.sh remove myguild --purge       # 连数据一起删

旧版单实例部署（根目录 .env + data/）会在首次运行任意命令时自动迁移为
instances/default（数据不动），之后用 ./deploy.sh up default 启动。
EOF
}

# pick_instance 交互式选择一个实例（菜单用）。
pick_instance() {
  local names n
  names="$(list_instances)"
  if [[ -z "$names" ]]; then
    warn "还没有任何实例"
    return 1
  fi
  printf '现有实例：%s\n' "$names"
  read -r -p "实例名: " n || return 1
  [[ -n "$n" ]] || return 1
  printf '%s' "$n"
}

interactive_menu() {
  while true; do
    cat <<EOF

${C_BOLD}KOOK Ticket 部署菜单${C_RESET}
  1) 新增实例并启动（向导）
  2) 启动 / 重新构建（全部实例）
  3) 升级（备份 + 重建 + 健康检查，逐实例滚动）
  4) 查看状态（全部实例）
  5) 查看日志（选择实例）
  6) 备份数据（全部实例）
  7) 从备份恢复（选择实例）
  8) 重置 WebUI 密码（选择实例）
  9) 环境自检
  l) 列出实例
  r) 移除实例
  0) 退出
EOF
    local choice n
    read -r -p "请选择: " choice || exit 0
    case "$choice" in
      1) cmd_add "" ;;
      2) cmd_up "" ;;
      3) cmd_upgrade "" ;;
      4) cmd_status "" ;;
      5) if n="$(pick_instance)"; then cmd_logs "$n"; fi ;;
      6) cmd_backup "" ;;
      7) if n="$(pick_instance)"; then cmd_restore "$n" ""; fi ;;
      8) if n="$(pick_instance)"; then cmd_reset_password "$n" ""; fi ;;
      9) cmd_doctor ;;
      l|L) cmd_list ;;
      r|R) if n="$(pick_instance)"; then cmd_remove "$n"; fi ;;
      0|"") exit 0 ;;
      *) warn "无效选择：$choice" ;;
    esac
  done
}

main() {
  local command="${1:-}"
  [[ $# -gt 0 ]] && shift || true

  # 选项先回到默认值再解析（交互菜单直接调用 cmd_* 时引用的是这里的默认值）
  OPT_PORT=""; OPT_BIND=""; OPT_TZ=""; OPT_PASSWORD=""; OPT_DRYRUN=""
  OPT_TOKEN=""; OPT_GUILD=""; OPT_INSTANCE=""
  OPT_FORCE="0"; OPT_NOBACKUP="0"; OPT_PURGE="0"; ASSUME_YES="0"
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
      --token) OPT_TOKEN="${2:-}"; shift 2 ;;
      --token=*) OPT_TOKEN="${1#*=}"; shift ;;
      --guild) OPT_GUILD="${2:-}"; shift 2 ;;
      --guild=*) OPT_GUILD="${1#*=}"; shift ;;
      --instance|-i) OPT_INSTANCE="${2:-}"; shift 2 ;;
      --instance=*) OPT_INSTANCE="${1#*=}"; shift ;;
      --dry-run|--dryrun) OPT_DRYRUN="1"; shift ;;
      --force) OPT_FORCE="1"; shift ;;
      --purge) OPT_PURGE="1"; shift ;;
      --no-backup) OPT_NOBACKUP="1"; shift ;;
      --yes|-y) ASSUME_YES="1"; shift ;;
      -h|--help) usage; exit 0 ;;
      --*) die "未知选项：$1（用 ./deploy.sh help 查看用法）" ;;
      -?*) die "未知选项：$1。长选项请用两个连字符，例如 --password（用 ./deploy.sh help 查看用法）" ;;
      *) positional+=("$1"); shift ;;
    esac
  done

  if [[ -z "$command" ]]; then
    if [[ -t 0 ]]; then interactive_menu; else usage; fi
    return 0
  fi
  case "$command" in
    help|-h|--help) usage; return 0 ;;
  esac

  # docker 探测：除 help/config 外的所有命令都需要 Docker
  # （旧脚本只在 doctor 里探测，导致 sudo 前缀在 up/backup 等命令中从不生效）
  case "$command" in
    config|env) ;;
    *) detect_docker ;;
  esac

  # 旧版单实例部署自动迁移（幂等）
  maybe_migrate

  # 实例寻址：--instance 优先；否则多数命令的第一个位置参数就是实例名。
  # restore / reset-password 的位置参数含义随 --instance 切换（见下）。
  case "$command" in
    add) cmd_add "${positional[0]:-}" ;;
    init) cmd_init "${positional[0]:-}" ;;
    migrate) ok "迁移检查完成（无旧版部署需要迁移，或已完成迁移）" ;;
    list|ls) cmd_list ;;
    remove|rm) cmd_remove "${positional[0]:-}" ;;
    up|start) cmd_up "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    upgrade|update) cmd_upgrade "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    restart) cmd_restart "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    stop) cmd_stop "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    down) cmd_down "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    status|ps) cmd_status "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    logs|log) cmd_logs "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    backup) cmd_backup "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    restore)
      if [[ -n "$OPT_INSTANCE" ]]; then
        cmd_restore "$OPT_INSTANCE" "${positional[0]:-}"
      else
        cmd_restore "${positional[0]:-}" "${positional[1]:-}"
      fi
      ;;
    reset-password|reset)
      if [[ -n "$OPT_INSTANCE" ]]; then
        cmd_reset_password "$OPT_INSTANCE" "${positional[0]:-}"
      else
        cmd_reset_password "${positional[0]:-}" "${positional[1]:-}"
      fi
      ;;
    config|env) cmd_config "${OPT_INSTANCE:-${positional[0]:-}}" ;;
    doctor|check) cmd_doctor ;;
    *) usage; die "未知命令：$command" ;;
  esac
}

main "$@"
