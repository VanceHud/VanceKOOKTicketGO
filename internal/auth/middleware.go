package auth

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/secure"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// gin.Context 中使用的键名集中定义，避免字符串散落各处。
const (
	CtxIdentity     = "kt.identity"
	CtxRequestID    = "kt.requestID"
	CtxClientIP     = "kt.clientIP"
	HeaderRequestID = "X-Request-Id"
	HeaderCSRF      = "X-CSRF-Token"
)

// Identity 是当前请求的登录身份。
type Identity struct {
	UserID             uint   `json:"userId"`
	Username           string `json:"username"`
	DisplayName        string `json:"displayName"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"mustChangePassword"`

	SessionID uint   `json:"-"`
	TokenHash string `json:"-"`
	// CSRFToken 是当前会话对应的 CSRF 令牌（由服务端密钥派生，见 SessionManager.CSRFToken）。
	CSRFToken string `json:"-"`
}

// IsAdmin 判断是否为管理员。
func (i *Identity) IsAdmin() bool { return i != nil && i.Role == store.RoleAdmin }

// CanOperate 判断是否具备工单写操作权限（管理员或客服）。
func (i *Identity) CanOperate() bool { return i != nil && store.RoleAtLeast(i.Role, store.RoleStaff) }

// IdentityOf 取出当前身份，未登录时返回 nil。
func IdentityOf(c *gin.Context) *Identity {
	value, ok := c.Get(CtxIdentity)
	if !ok {
		return nil
	}
	identity, _ := value.(*Identity)
	return identity
}

// RequestIDOf 取出当前请求 ID。
func RequestIDOf(c *gin.Context) string {
	if value, ok := c.Get(CtxRequestID); ok {
		if id, ok := value.(string); ok {
			return id
		}
	}
	return ""
}

// ClientIPOf 返回解析后的客户端 IP（已考虑可信反代）。
func ClientIPOf(c *gin.Context) string {
	if value, ok := c.Get(CtxClientIP); ok {
		if ip, ok := value.(string); ok {
			return ip
		}
	}
	return c.ClientIP()
}

// RequestID 为每个请求生成或透传请求 ID。
//
// 入站值会被严格过滤：只保留字母、数字与短横线，长度上限 32，
// 避免把外部字符串原样回显到响应头中造成头注入或日志污染。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := sanitizeRequestID(c.GetHeader(HeaderRequestID))
		if id == "" {
			if generated, err := secure.RandomHex(8); err == nil {
				id = generated
			} else {
				id = "unknown"
			}
		}
		c.Set(CtxRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

func sanitizeRequestID(raw string) string {
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	return b.String()
}

// Recover 捕获 panic：堆栈只写入服务端日志，响应体只包含请求 ID。
func Recover(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Error("请求处理发生 panic",
					"request_id", RequestIDOf(c),
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				if !c.Writer.Written() {
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
						"error": gin.H{
							"code":      "internal_error",
							"message":   "服务器内部错误，请稍后重试或联系管理员",
							"requestId": RequestIDOf(c),
						},
					})
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}

// SecurityHeaders 统一设置安全响应头。
//
// CSP 说明：脚本仅允许同源（Vite 产物为外部文件，无 inline script）；
// style-src 允许 'unsafe-inline' 是因为组件库会通过 style 属性设置图表颜色等，
// 该让步不引入脚本执行能力。
func SecurityHeaders() gin.HandlerFunc {
	const csp = "default-src 'self'; base-uri 'self'; form-action 'self'; " +
		"frame-ancestors 'none'; object-src 'none'; script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; " +
		"font-src 'self' data:; connect-src 'self'"
	return func(c *gin.Context) {
		header := c.Writer.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=()")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Content-Security-Policy", csp)
		c.Next()
	}
}

// HSTS 在 HTTPS 请求下追加 HSTS 头。
func HSTS(c *gin.Context) {
	if IsHTTPS(c) {
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

// IsHTTPS 判断当前请求是否为 HTTPS。
//
// 仅当直连对端是可信反代时才采信 X-Forwarded-Proto，避免被伪造。
func IsHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		if trusted, ok := c.Get(ctxTrustedPeer); ok {
			if isTrusted, _ := trusted.(bool); isTrusted {
				return strings.EqualFold(strings.TrimSpace(proto), "https")
			}
		}
	}
	return false
}

const ctxTrustedPeer = "kt.trustedPeer"

// ClientIP 解析真实客户端 IP 并写入 context。
//
// 安全要点：只有直连对端命中 TRUSTED_PROXIES 时才采信 X-Forwarded-For，
// 否则一律使用 RemoteAddr，防止伪造头绕过限流与审计。
func ClientIP(trustedProxies []string) gin.HandlerFunc {
	nets := parseTrustedProxies(trustedProxies)
	return func(c *gin.Context) {
		directIP := remoteIP(c.Request)
		trustedPeer := ipInRanges(directIP, nets)
		c.Set(ctxTrustedPeer, trustedPeer)

		resolved := directIP
		if trustedPeer {
			if forwarded := forwardedIP(c.Request, nets); forwarded != "" {
				resolved = forwarded
			}
		}
		c.Set(CtxClientIP, resolved)
		c.Next()
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// forwardedIP 从右向左跳过可信代理，返回第一个不可信地址。
func forwardedIP(r *http.Request, nets []*net.IPNet) string {
	chains := []string{}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		chains = append(chains, strings.Split(xff, ",")...)
	}
	if real := r.Header.Get("X-Real-Ip"); real != "" {
		chains = append(chains, real)
	}
	for i := len(chains) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(chains[i])
		if candidate == "" {
			continue
		}
		ip := net.ParseIP(candidate)
		if ip == nil {
			// 非法值直接忽略，避免污染审计记录。
			continue
		}
		if !ipInRangesIP(ip, nets) {
			return ip.String()
		}
	}
	return ""
}

func parseTrustedProxies(entries []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			out = append(out, network)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

func ipInRanges(ipStr string, nets []*net.IPNet) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	return ipInRangesIP(ip, nets)
}

func ipInRangesIP(ip net.IP, nets []*net.IPNet) bool {
	if len(nets) == 0 || ip == nil {
		return false
	}
	for _, network := range nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// Load 从 Cookie 读取会话并填充 Identity；未登录时不做拦截。
func (m *SessionManager) Load(cookieName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(cookieName)
		if err != nil || token == "" {
			c.Next()
			return
		}
		session, user, err := m.Authenticate(token)
		if err != nil {
			// 失效会话直接清 Cookie 与登录标记，界面会回到登录页。
			clearSessionCookie(c, cookieName)
			ClearLoggedInMarker(c)
			c.Next()
			return
		}
		identity := &Identity{
			UserID:             user.ID,
			Username:           user.Username,
			DisplayName:        user.DisplayName,
			Role:               user.Role,
			MustChangePassword: user.MustChangePassword,
			SessionID:          session.ID,
			TokenHash:          session.TokenHash,
			CSRFToken:          m.CSRFToken(token),
		}
		c.Set(CtxIdentity, identity)
		c.Next()
	}
}

// RequireAuth 要求已登录。
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if IdentityOf(c) == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "unauthenticated", "message": "请先登录"},
			})
			return
		}
		c.Next()
	}
}

// passwordChangeAllowlist 列出强制改密期间仍然允许访问的接口。
var passwordChangeAllowlist = map[string]struct{}{
	"/api/v1/auth/me":       {},
	"/api/v1/auth/password": {},
	"/api/v1/auth/logout":   {},
}

// RequirePasswordChanged 在账号处于“必须改密”状态时，只放行改密相关接口。
//
// 对应安全基线第 1 条：首次启动随机密码必须改密后才可使用其它功能。
func RequirePasswordChanged() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := IdentityOf(c)
		if identity != nil && identity.MustChangePassword {
			if _, allowed := passwordChangeAllowlist[c.Request.URL.Path]; !allowed {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": gin.H{
						"code":    "password_change_required",
						"message": "首次登录必须修改初始密码后才能继续使用",
					},
				})
				return
			}
		}
		c.Next()
	}
}

// RequireCSRF 校验非幂等请求的 CSRF 令牌。
//
// 采用“同步器令牌”模式：令牌由 HMAC(APP_SECRET, 会话 token) 派生，
// 客户端从 /auth/me 获取后通过 X-CSRF-Token 头回传，不落库、无需轮换。
// 对比使用恒定时间比较，避免时序侧信道。
func (m *SessionManager) RequireCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		identity := IdentityOf(c)
		if identity == nil {
			// 未登录的写请求由 RequireAuth 处理。
			c.Next()
			return
		}
		if !secure.ConstantTimeEqual(identity.CSRFToken, c.GetHeader(HeaderCSRF)) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "csrf_failed", "message": "CSRF 校验失败，请刷新页面后重试"},
			})
			return
		}
		c.Next()
	}
}

// RequireRole 要求当前账号权限不低于 minRole。
func RequireRole(minRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := IdentityOf(c)
		if identity == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "unauthenticated", "message": "请先登录"},
			})
			return
		}
		if !store.RoleAtLeast(identity.Role, minRole) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "forbidden", "message": "当前账号没有执行该操作的权限"},
			})
			return
		}
		c.Next()
	}
}

// LoggedInMarkerCookie 是“是否可能已登录”的提示 Cookie。
//
// 安全说明：它**不是凭据**，值恒为 "1"，且刻意不带 HttpOnly，
// 只为让前端在未登录时跳过 /auth/me 请求，避免浏览器控制台出现无意义的 401。
// 是否真的已认证始终以服务端会话校验结果为准。
const LoggedInMarkerCookie = "kt_logged_in"

// SetLoggedInMarker 写入登录提示 Cookie（与前端读取保持同样的属性）。
func SetLoggedInMarker(c *gin.Context, maxAge time.Duration) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     LoggedInMarkerCookie,
		Value:    "1",
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: false,
		Secure:   IsHTTPS(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearLoggedInMarker 清除登录提示 Cookie。
func ClearLoggedInMarker(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     LoggedInMarkerCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   IsHTTPS(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// SetSessionCookie 写入会话 Cookie。
func SetSessionCookie(c *gin.Context, cookieName, token string, maxAge time.Duration) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   IsHTTPS(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie 清除会话 Cookie。
func clearSessionCookie(c *gin.Context, cookieName string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   IsHTTPS(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie 供登出接口使用。
func ClearSessionCookie(c *gin.Context, cookieName string) { clearSessionCookie(c, cookieName) }
