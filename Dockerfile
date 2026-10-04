# ---------------------------------------------------------------------------
# 三阶段构建：前端（Node）→ 后端（Go，纯静态）→ 运行镜像（非 root，最小依赖）
#
# 为什么分阶段：
#   - 运行镜像里没有 Node、npm、node_modules 与源码，攻击面更小；
#   - CGO_ENABLED=0 得到静态二进制，可运行在 alpine 等最小基础镜像上；
#   - 前端产物通过 //go:embed 打进二进制，最终只需分发一个文件。
# ---------------------------------------------------------------------------

# ---------- 阶段 1：构建前端 ----------
FROM node:22-alpine AS frontend
WORKDIR /src

# 先只拷贝依赖清单，最大化利用 Docker 层缓存
COPY web/frontend/package.json web/frontend/package-lock.json ./
# 使用 npm ci：严格按 lockfile 安装，保证构建可复现
RUN npm ci

COPY web/frontend/ ./
RUN npm run build


# ---------- 阶段 2：编译后端 ----------
FROM golang:1.26-alpine AS backend
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 用阶段 1 的真实产物覆盖占位目录，再编译进二进制
RUN rm -rf ./web/dist && mkdir -p ./web/dist
COPY --from=frontend /src/dist ./web/dist

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags="-s -w -X main.Version=${VERSION}" \
        -o /out/kook-ticket ./cmd/server


# ---------- 阶段 3：运行镜像 ----------
FROM alpine:3.22

# 仅安装时区数据（工单编号按 TICKET_TZ 计算日期）与根证书（访问 KOOK HTTPS 接口）
RUN apk add --no-cache tzdata ca-certificates \
    && adduser -D -u 10001 -h /app app

WORKDIR /app
# 直接用 --chown 拷贝，避免后续对整个 /app 执行 chown 时复制二进制层（会凭空多出一份镜像体积）
COPY --from=backend --chown=10001:10001 /out/kook-ticket /app/kook-ticket

# 数据目录是唯一需要写入的路径，这里只对空目录设权限
RUN mkdir -p /app/data && chown 10001:10001 /app/data

# 以非 root 运行；数据目录是唯一需要写入的路径
USER 10001

ENV PORT=8080 \
    DATA_DIR=/app/data \
    TICKET_TZ=Asia/Shanghai \
    LOG_LEVEL=info

EXPOSE 8080
VOLUME ["/app/data"]

# busybox wget 随 alpine 提供，无需额外安装工具
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/kook-ticket"]
