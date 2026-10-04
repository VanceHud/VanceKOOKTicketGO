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
	"strings"
	"syscall"
	"time"

	"vancekookticket/internal/api"
	"vancekookticket/internal/auth"
	"vancekookticket/internal/bootstrap"
	"vancekookticket/internal/bot"
	"vancekookticket/internal/config"
	"vancekookticket/internal/dryrun"
	"vancekookticket/internal/eventbus"
	"vancekookticket/internal/secure"
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
	// 默认使用空实现：未配置 Token（或 DryRun）时，工单操作只更新数据库并广播事件。
	// 机器人连接成功后会自动注入真实平台实现（见 bot.Bot.Start）。
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 机器人管理器：读取数据库中的配置（Token 已加密存储）并按需连接 KOOK。
	botManager, err := bot.NewManager(bot.Deps{
		Store:     st,
		Bus:       bus,
		Tickets:   ticketService,
		Config:    runtimeConfigProvider(st, cfg.AppSecret),
		Logger:    logger,
		Location:  cfg.Location,
		AppSecret: cfg.AppSecret,
		// /kill 命令触发与 Ctrl+C 相同的优雅退出路径
		RequestStop:   stop,
		HeartbeatTick: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("初始化机器人失败: %w", err)
	}
	defer botManager.Stop()

	router := api.NewRouter(api.Deps{
		Config:   cfg,
		Store:    st,
		Bus:      bus,
		Tickets:  ticketService,
		Sessions: sessions,
		Login:    loginLimiter,
		Codes:    codeLimiter,
		Web:      webHandler,
		Log:      logger,
		Started:  store.Now(),
		Version:  Version,
		Bot:      botManager,
	})

	// 启动机器人；失败不阻塞 WebUI —— 管理员可在界面上修正配置后点击重连。
	if cfg.DryRun {
		logger.Info("DryRun 模式：不连接 KOOK，工单操作仅更新数据库")
	} else if err := botManager.Start(ctx); err != nil {
		logger.Warn("机器人启动失败，可通过 WebUI「机器人状态 → 重新连接」重试", "err", err)
	}

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

// runtimeConfigProvider 把数据库配置转换成机器人运行配置。
//
// Token 以 AES-GCM 密文存储，这里按需解密；解密失败（密钥被更换）时返回空 Token，
// 让机器人停在“未配置”状态，界面会提示重新填写。
func runtimeConfigProvider(st *store.Store, appSecret []byte) bot.ConfigFunc {
	return func(ctx context.Context) (*bot.Config, error) {
		token, _, err := st.Settings.GetSecret(store.SettingKookToken, appSecret)
		if err != nil && !errors.Is(err, secure.ErrDecrypt) {
			return nil, err
		}
		return &bot.Config{
			Token:          token,
			GuildID:        settingValue(st, store.SettingGuildID),
			CategoryID:     settingValue(st, store.SettingCategoryID),
			LogChannelID:   settingValue(st, store.SettingLogChannelID),
			DebugChannelID: settingValue(st, store.SettingDebugChannelID),
			OutdateHours:   st.Settings.OutdateHours(),
		}, nil
	}
}

func settingValue(st *store.Store, key string) string {
	value, err := st.Settings.GetDefault(key, "")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
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
