// Command server 是 KOOK Ticket 机器人与 WebUI 的唯一入口。
//
// 一个进程同时提供：WebUI 静态资源与接口、后台任务（会话清理、超时工单扫描），
// 以及（里程碑 2 起）KOOK WebSocket 连接。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vancekookticket/internal/api"
	"vancekookticket/internal/auth"
	"vancekookticket/internal/bootstrap"
	"vancekookticket/internal/config"
	"vancekookticket/internal/dryrun"
	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
	"vancekookticket/web"
)

// Version 是构建版本号，可在构建时通过 -ldflags "-X main.Version=..." 覆盖。
var Version = "0.1.0-milestone1"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	logger.Info("启动 KOOK Ticket",
		"version", Version,
		"dry_run", cfg.DryRun,
		"data_dir", cfg.DataDir,
		"ticket_tz", cfg.TicketTZ,
		"secret_source", cfg.SecretSource,
	)
	if cfg.KookToken != "" {
		logger.Info("检测到环境变量 KOOK_TOKEN，将在启动时写入加密配置")
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if err := st.Migrate(); err != nil {
		return err
	}
	if result, err := st.QuickCheck(); err != nil {
		logger.Error("数据库自检未通过，请检查数据文件", "result", result, "err", err)
	} else {
		logger.Debug("数据库自检通过", "path", cfg.DBPath)
	}

	initResult, err := bootstrap.Init(st, cfg, logger)
	if err != nil {
		return err
	}
	if initResult.InitialPassword != "" {
		// 只在首次生成时打印一次；日志中不包含其它敏感信息。
		logger.Warn("已生成初始管理员账号，请立即登录并修改密码",
			"username", cfg.AdminUsername,
			"initial_password", initResult.InitialPassword,
			"note", "登录后会被强制要求修改密码",
		)
	}

	bus := eventbus.New()
	// 里程碑 2/3 会在这里注入真实 KOOK 客户端；当前使用空实现，
	// 业务流程（关单/锁定/重开）在 DryRun 下只更新数据库并广播事件。
	platform := ticket.NewNoopPlatform(logger)
	ticketService := ticket.NewService(st, bus, platform, cfg.Location, st.Settings.OutdateHours)

	sessions := auth.NewSessionManager(st, cfg.SessionIdleTTL, cfg.SessionMaxTTL, cfg.AppSecret)
	loginLimiter := auth.NewLoginLimiter(cfg.LoginMaxFails, cfg.LoginWindow, cfg.LoginLockFor)
	codeLimiter := auth.NewWindowLimiter(10, 10*time.Minute)

	webHandler, err := web.New()
	if err != nil {
		return fmt.Errorf("初始化静态资源失败: %w", err)
	}
	if !webHandler.HasIndex() {
		logger.Warn("二进制中未内嵌前端产物，访问首页会提示构建步骤",
			"hint", "cd web/frontend && npm ci && npm run build")
	}

	if cfg.DryRun {
		if err := dryrun.Seed(st, cfg.Location, logger); err != nil {
			logger.Error("写入演示数据失败", "err", err)
		}
	}

	router := api.NewRouter(api.Deps{
		Config:       cfg,
		Store:        st,
		Bus:          bus,
		Tickets:      ticketService,
		Sessions:     sessions,
		Login:        loginLimiter,
		Codes:        codeLimiter,
		Web:          webHandler,
		Log:          logger,
		Started:      store.Now(),
		Version:      Version,
		BotConnected: func() bool { return false }, // 里程碑 3 接入 KOOK 后返回真实状态
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go backgroundTasks(ctx, logger, st, sessions, loginLimiter, codeLimiter, ticketService)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// SSE 是长连接，写超时必须为 0，否则连接会被服务端主动中断。
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务已启动", "addr", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关闭超时", "err", err)
	}
	logger.Info("已退出")
	return nil
}

// backgroundTasks 运行周期性维护任务。
func backgroundTasks(
	ctx context.Context,
	logger *slog.Logger,
	st *store.Store,
	sessions *auth.SessionManager,
	loginLimiter *auth.LoginLimiter,
	codeLimiter *auth.WindowLimiter,
	ticketService *ticket.Service,
) {
	maintenance := time.NewTicker(5 * time.Minute)
	timeoutScan := time.NewTicker(10 * time.Minute)
	defer maintenance.Stop()
	defer timeoutScan.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-maintenance.C:
			if n, err := sessions.GC(); err != nil {
				logger.Error("清理过期会话失败", "err", err)
			} else if n > 0 {
				logger.Debug("清理过期会话", "count", n)
			}
			if n, err := st.Codes.DeleteExpired(store.Now()); err != nil {
				logger.Error("清理过期一次性码失败", "err", err)
			} else if n > 0 {
				logger.Debug("清理过期一次性码", "count", n)
			}
			loginLimiter.GC()
			codeLimiter.GC()
		case <-timeoutScan.C:
			locked, err := ticketService.ScanTimeout(ctx)
			if err != nil {
				logger.Error("超时工单扫描失败", "err", err)
				continue
			}
			if len(locked) > 0 {
				logger.Info("已自动锁定超时工单", "count", len(locked), "tickets", locked)
			}
		}
	}
}
