// Package bootstrap 负责首次启动时的初始化：管理员账号与来自环境变量的业务配置。
package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/auth"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/config"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// Result 描述本次初始化的结果。
type Result struct {
	// InitialPassword 非空表示本次生成了随机初始密码，调用方需要打印一次并提示用户改密。
	InitialPassword string
	// AdminCreated 表示本次创建了管理员账号。
	AdminCreated bool
	// TokenLoaded 表示本次从环境变量装载了 KOOK token（已加密存库）。
	TokenLoaded bool
	// GuildIDLoaded 表示本次从环境变量装载了服务器 ID。
	GuildIDLoaded bool
}

// Init 执行初始化，保证：
//   - 至少存在一个管理员账号（已有账号时不会覆盖其密码）；
//   - 环境变量提供的 KOOK token / 服务器 ID 会被加密写入配置（不覆盖界面已保存的值）；
//   - 记录首次初始化时间。
func Init(st *store.Store, cfg *config.Config, log *slog.Logger) (*Result, error) {
	result := &Result{}

	created, password, err := ensureAdmin(st, cfg)
	if err != nil {
		return nil, err
	}
	result.AdminCreated = created
	result.InitialPassword = password

	// 环境变量优先：仅在提供了 KOOK_TOKEN 时写入（覆盖旧值，便于轮换 token）。
	if cfg.KookTokenFromEnv {
		if err := st.Settings.SetSecret(store.SettingKookToken, cfg.KookToken, cfg.AppSecret); err != nil {
			return nil, fmt.Errorf("保存 KOOK token 失败: %w", err)
		}
		result.TokenLoaded = true
		log.Info("已从环境变量装载 KOOK token", "masked", config.RedactsToken(cfg.KookToken))
	}
	if id := strings.TrimSpace(cfg.KookGuildID); id != "" {
		existing, ok, err := st.Settings.Get(store.SettingGuildID)
		if err != nil {
			return nil, err
		}
		if !ok || existing == "" {
			if err := st.Settings.Set(store.SettingGuildID, id); err != nil {
				return nil, err
			}
			result.GuildIDLoaded = true
		}
	}

	if err := st.Settings.MarkInitialized(); err != nil {
		return nil, err
	}
	return result, nil
}

// ensureAdmin 保证存在管理员账号。
//
// 规则：
//   - 已有任意账号时直接返回，不做任何修改（避免重启把管理员密码重置）；
//   - 提供 ADMIN_PASSWORD 时使用该密码，但必须满足强度要求（启动即失败，避免弱口令）；
//   - 未提供时生成随机初始密码，并要求首次登录后改密。
func ensureAdmin(st *store.Store, cfg *config.Config) (created bool, initialPassword string, err error) {
	count, err := st.Users.Count()
	if err != nil {
		return false, "", fmt.Errorf("统计账号数量失败: %w", err)
	}
	if count > 0 {
		return false, "", nil
	}

	password := strings.TrimSpace(cfg.AdminPassword)
	generated := false
	if password == "" {
		password, err = auth.GeneratePassword()
		if err != nil {
			return false, "", fmt.Errorf("生成初始管理员密码失败: %w", err)
		}
		generated = true
	} else if err := auth.ValidatePasswordStrength(password); err != nil {
		return false, "", fmt.Errorf("ADMIN_PASSWORD 不满足强度要求: %w", err)
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, "", err
	}

	user := &store.WebUser{
		Username:     cfg.AdminUsername,
		DisplayName:  "初始管理员",
		PasswordHash: hash,
		Role:         store.RoleAdmin,
		// 随机生成的密码必须首登修改；显式配置的密码由管理员自己负责，不强制。
		MustChangePassword: generated,
	}
	if err := st.Users.Create(user); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, "", err
		}
		return false, "", fmt.Errorf("创建管理员账号失败: %w", err)
	}

	_ = st.Audit.Write(&store.AuditLog{
		Actor:     "system",
		ActorType: store.ActorTypeBot,
		Action:    "system.bootstrap",
		Target:    user.Username,
		Detail:    "首次启动创建初始管理员账号",
		CreatedAt: store.Now(),
	})

	if generated {
		return true, password, nil
	}
	return true, "", nil
}
