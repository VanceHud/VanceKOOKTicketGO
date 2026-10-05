package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/eventbus"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

func TestAPIBodyLimitsAndJSONContentType(t *testing.T) {
	env := newTestEnv(t)
	for _, length := range []int64{maxRequestBodyBytes + 1, -1} {
		// 完整 JSON 后附加数据也必须被拒绝，不能只读到第一个 JSON 对象。
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"x"}`+strings.Repeat(" ", maxRequestBodyBytes)))
		req.ContentLength = length
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		env.engine.ServeHTTP(res, req)
		if res.Code != http.StatusRequestEntityTooLarge || !strings.Contains(res.Body.String(), "request_too_large") {
			t.Fatalf("length=%d: %d %s", length, res.Code, res.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"x"}`))
	req.Header.Set("Content-Type", "text/plain")
	res := httptest.NewRecorder()
	env.engine.ServeHTTP(res, req)
	if res.Code != http.StatusUnsupportedMediaType || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("跨站简单请求应被拒绝且禁止缓存: %d %v", res.Code, res.Header())
	}
}

type roleVerificationBot struct {
	botAdapter
	role string
	ok   bool
	err  error
}

func (b roleVerificationBot) ResolveWebRole(context.Context, string) (string, bool, error) {
	return b.role, b.ok, b.err
}

func TestLoginCodeNeverFallsBackWhenCurrentRoleCannotBeVerified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bot    BotController
		status int
	}{
		{"removed mapping", roleVerificationBot{botAdapter: botAdapter{newFakeBot()}}, http.StatusForbidden},
		{"upstream failure", roleVerificationBot{botAdapter: botAdapter{newFakeBot()}, err: errors.New("offline")}, http.StatusServiceUnavailable},
		{"missing bot", nil, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.config.DryRun = false
			env.deps.Bot = tc.bot
			env.engine = NewRouter(env.deps)
			code := issueCode(t, env, "OLDADM", store.CodePurposeLogin, "9001", "原管理员", store.RoleAdmin, time.Minute)
			res := env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "OLDADM"})
			if res.status != tc.status || len(res.cookies) != 0 {
				t.Fatalf("旧管理员快照不能授权: %d %s", res.status, res.raw)
			}
			if _, err := env.store.Users.ByKookID("9001"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("不能创建账号: %v", err)
			}
			stored, err := env.store.Codes.ByCodeHash(code.CodeHash)
			if err != nil || stored.UsedAt != nil {
				t.Fatalf("权限校验失败不能提前消费码: %v", err)
			}
		})
	}
}

func TestLoginCodeRoleChangeRevokesExistingSessions(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "backup", adminPassword, store.RoleAdmin, false)
	user := env.seedUser(t, "mapped", adminPassword, store.RoleAdmin, false)
	kookID := "9001"
	if err := env.store.Users.UpdateFields(user.ID, map[string]any{"kook_user_id": kookID}); err != nil {
		t.Fatal(err)
	}
	cookie, _ := env.login(t, "mapped", adminPassword)
	env.deps.Bot = roleVerificationBot{botAdapter: botAdapter{newFakeBot()}, role: store.RoleReadonly, ok: true}
	env.engine = NewRouter(env.deps)
	issueCode(t, env, "LOWROL", store.CodePurposeLogin, kookID, "小林", store.RoleAdmin, time.Minute)
	res := env.do(t, http.MethodPost, "/api/v1/auth/login-code", map[string]string{"code": "LOWROL"})
	if res.status != http.StatusOK || res.body["user"].(map[string]any)["role"] != store.RoleReadonly {
		t.Fatalf("%d %s", res.status, res.raw)
	}
	if old := env.do(t, http.MethodGet, "/api/v1/auth/me", nil, withCookie(cookie)); old.status != http.StatusUnauthorized {
		t.Fatalf("角色变化后旧会话仍可用: %d", old.status)
	}
}

func TestForcedPasswordChangeBlocksBinding(t *testing.T) {
	env := newTestEnv(t)
	user := env.seedUser(t, "admin", adminPassword, store.RoleAdmin, true)
	cookie, csrf := env.login(t, "admin", adminPassword)
	code := issueCode(t, env, "BINDOK", store.CodePurposeBind, "9001", "小林", "", time.Minute)
	res := env.do(t, http.MethodPost, "/api/v1/auth/bind-code", map[string]string{"code": "BINDOK"}, withCookie(cookie), withCSRF(csrf))
	updated, _ := env.store.Users.ByID(user.ID)
	stored, _ := env.store.Codes.ByCodeHash(code.CodeHash)
	if res.status != http.StatusForbidden || updated.KookUserID != nil || stored.UsedAt != nil {
		t.Fatalf("未改密不能绑定: %d %s", res.status, res.raw)
	}
}

func TestTicketOperationsFailWhenPlatformIsUnavailable(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, csrf := env.login(t, "admin", adminPassword)
	ticket := env.seedTicket(t, store.TicketOpen)
	env.tickets.SetPlatform(nil)
	for _, action := range []string{"lock", "close"} {
		res := env.do(t, http.MethodPost, "/api/v1/tickets/"+ticket.No+"/"+action, map[string]string{}, withCookie(cookie), withCSRF(csrf))
		if res.status != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d %s", action, res.status, res.raw)
		}
	}
	updated, _ := env.store.Tickets.ByNo(ticket.No)
	if updated.Status != store.TicketOpen {
		t.Fatal("离线操作不能改变工单状态")
	}
}

func TestExportsDoNotTruncateAndEscapeSpreadsheetFormulas(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "admin", adminPassword, store.RoleAdmin, false)
	cookie, _ := env.login(t, "admin", adminPassword)
	ticket := env.seedTicket(t, store.TicketOpen)
	messages := make([]store.TicketMessage, 2001)
	for i := range messages {
		messages[i] = store.TicketMessage{TicketNo: ticket.No, Content: fmt.Sprintf("message-%d", i), CreatedAt: store.Now(), MsgType: store.MsgTypeText}
	}
	messages[0].Content = " =HYPERLINK(\"https://example.test\")"
	messages[0].UserName = "+1+1"
	if err := env.store.DB().CreateInBatches(messages, 100).Error; err != nil {
		t.Fatal(err)
	}
	res := env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/export?format=json", nil, withCookie(cookie))
	var payload struct {
		Messages []store.TicketMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(res.raw), &payload); err != nil {
		t.Fatal(err)
	}
	if res.status != http.StatusOK || len(payload.Messages) != len(messages) {
		t.Fatalf("导出应含全部消息: %d / %d", len(payload.Messages), len(messages))
	}
	res = env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/export?format=csv", nil, withCookie(cookie))
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(res.raw, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][3] != "'+1+1" || rows[1][6] != "'"+messages[0].Content {
		t.Fatalf("公式未转为文本: %v", rows[1])
	}
	for _, dangerous := range []string{"=1+1", "+cmd", "-cmd", "@SUM(1)", " \t=1", "\ttext", "\rtext"} {
		if csvText(dangerous) != "'"+dangerous {
			t.Errorf("漏拦截 %q", dangerous)
		}
	}
}

func TestExportMediaRejectsActiveSchemes(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "data:text/html,<script>alert(1)</script>", "file:///etc/passwd", "//example.test/x", "https://user:pass@example.test/x"} {
		for _, kind := range []string{store.MsgTypeImage, store.MsgTypeFile, store.MsgTypeCard, store.MsgTypeAudio, store.MsgTypeVideo} {
			msg := store.TicketMessage{MsgType: kind, MediaURL: raw, Content: "附件"}
			if mediaURL(msg) != "" || strings.Contains(renderHTMLMessage(msg), "href=") || strings.Contains(renderHTMLMessage(msg), "src=") {
				t.Fatalf("危险链接被渲染: %q", raw)
			}
		}
	}
	if got := mediaURL(store.TicketMessage{MediaURL: "https://example.test/file?a=1&b=2"}); got == "" {
		t.Fatal("有效 HTTPS 被拒绝")
	}
}

func TestSSERevocationStopsSensitiveEvents(t *testing.T) {
	env := newTestEnv(t)
	user := env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	cookie, _ := env.login(t, "staff", staffPassword)
	server := httptest.NewServer(env.engine)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	req.AddCookie(cookie)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
	}
	if err := env.deps.Sessions.RevokeUser(user.ID); err != nil {
		t.Fatal(err)
	}
	env.deps.Bus.Publish(eventbus.Event{Type: eventbus.EventTicketMessage, Data: "SECRET-AFTER-REVOCATION"})
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "SECRET-AFTER-REVOCATION") || !strings.Contains(string(body), "auth.expired") {
		t.Fatalf("吊销后数据泄露: %s", body)
	}
}

func TestSSEAccountConnectionLimit(t *testing.T) {
	env := newTestEnv(t)
	user := env.seedUser(t, "staff", staffPassword, store.RoleStaff, false)
	cookie, _ := env.login(t, "staff", staffPassword)
	for i := 0; i < 5; i++ {
		id, _, _ := env.deps.Bus.SubscribeLimited(strconv.Itoa(int(user.ID)), 5, 64)
		defer env.deps.Bus.Unsubscribe(id)
	}
	res := env.do(t, http.MethodGet, "/api/v1/events", nil, withCookie(cookie))
	if res.status != http.StatusTooManyRequests {
		t.Fatalf("连接上限未生效: %d", res.status)
	}
}
