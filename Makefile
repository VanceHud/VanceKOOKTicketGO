# KOOK Ticket 开发与构建入口
#
# 常用：
#   make dev-backend    以 DryRun 模式启动后端（默认 :8080，自动写入演示数据）
#   make dev-frontend   启动 Vite 开发服务器（代理 /api 到后端）
#   make build          构建前端并编译单二进制到 bin/kook-ticket
#   make docker         构建 Docker 镜像
#   make check          后端 vet + 前端类型检查与构建

SHELL := /bin/bash
BIN := bin/kook-ticket
PORT ?= 8080
DATA_DIR ?= ./data

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: dev-backend
dev-backend: ## 以 DryRun 模式启动后端（假数据，不需要 KOOK token）
	KOOK_DRYRUN=1 PORT=$(PORT) DATA_DIR=$(DATA_DIR) LOG_LEVEL=debug go run ./cmd/server

.PHONY: dev-frontend
dev-frontend: ## 启动前端开发服务器（Vite 代理 /api 到 127.0.0.1:8080）
	cd web/frontend && npm run dev

.PHONY: frontend
frontend: ## 构建前端并同步到 web/dist（供 go:embed 打包）
	cd web/frontend && npm run build
	@rm -rf web/dist
	@mkdir -p web/dist
	@cp -r web/frontend/dist/. web/dist/
	# 保留占位文件：新克隆的仓库在没有执行过 npm 构建时也能 go build（go:embed 需要至少匹配一个文件）
	@touch web/dist/.gitkeep
	@echo "前端产物已同步到 web/dist"

.PHONY: build
build: frontend ## 构建单二进制到 bin/kook-ticket
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o $(BIN) ./cmd/server
	@echo "已生成 $(BIN)"

.PHONY: run
run: build ## 构建并运行（生产模式，需要 KOOK_TOKEN）
	PORT=$(PORT) DATA_DIR=$(DATA_DIR) ./$(BIN)

.PHONY: dist
dist: frontend ## 交叉编译发布包（linux/amd64、linux/arm64、darwin/arm64）到 bin/
	@mkdir -p bin
	@version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev); \
	for target in linux/amd64 linux/arm64 darwin/arm64; do \
		os=$${target%%/*}; arch=$${target##*/}; \
		out=bin/kook-ticket-$$os-$$arch; \
		echo "→ $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags="-s -w -X main.Version=$$version" -o $$out ./cmd/server || exit 1; \
	done
	@echo "完成："
	@ls -lh bin/ | tail -n +2

.PHONY: check
check: ## 后端静态检查 + 前端类型检查与构建
	@files=$$(gofmt -l .); if [ -n "$$files" ]; then echo "存在未格式化的 Go 文件（已列出，可执行 gofmt -w .）:"; echo "$$files"; exit 1; fi
	go vet ./...
	go build ./...
	cd web/frontend && npm run build

.PHONY: test
test: ## 运行 Go 测试
	go test ./...

.PHONY: docker
docker: ## 构建 Docker 镜像（多阶段：Node 构建前端 → Go 编译 → alpine 运行）
	docker build -t kook-ticket:local .

.PHONY: compose-up
compose-up: ## 使用 docker compose 启动
	docker compose up -d --build

.PHONY: clean
clean: ## 清理构建产物
	rm -rf bin web/dist web/frontend/dist
	@mkdir -p web/dist && touch web/dist/.gitkeep
