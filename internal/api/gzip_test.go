package api

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/auth"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
)

// gzipBody 解开响应体；非 gzip 时原样返回。
func gzipBody(t *testing.T, res response) string {
	t.Helper()
	if res.headers.Get("Content-Encoding") != "gzip" {
		return res.raw
	}
	reader, err := gzip.NewReader(strings.NewReader(res.raw))
	if err != nil {
		t.Fatalf("解压响应失败: %v", err)
	}
	defer reader.Close()
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("读取解压内容失败: %v", err)
	}
	return string(plain)
}

// TestAPIResponsesAreGzippedWhenLarge 大响应在客户端支持时走 gzip，
// 且解压后内容与未压缩请求一致。
func TestAPIResponsesAreGzippedWhenLarge(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "reader", "ReaderPass123", store.RoleReadonly, false)
	cookie, csrf := env.login(t, "reader", "ReaderPass123")

	ticket := env.seedTicket(t, store.TicketOpen)
	// 填充足够多的消息，让响应超过压缩阈值。
	for i := 0; i < 30; i++ {
		if err := env.store.Tickets.AddMessage(&store.TicketMessage{
			TicketNo: ticket.No, UserID: "u1", UserName: "用户",
			Content: strings.Repeat("消息内容", 40), MsgType: store.MsgTypeText,
		}); err != nil {
			t.Fatal(err)
		}
	}

	plain := env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/messages?limit=30",
		nil, withCookie(cookie), withCSRF(csrf))
	if plain.headers.Get("Content-Encoding") != "" {
		t.Fatalf("未声明 gzip 的客户端不应收到压缩响应: %s", plain.headers.Get("Content-Encoding"))
	}
	if plain.headers.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("未压缩响应也应带 Vary: %q", plain.headers.Get("Vary"))
	}

	compressed := env.do(t, http.MethodGet, "/api/v1/tickets/"+ticket.No+"/messages?limit=30",
		nil, withCookie(cookie), withCSRF(csrf),
		func(req *http.Request) { req.Header.Set("Accept-Encoding", "gzip") })
	if compressed.headers.Get("Content-Encoding") != "gzip" {
		t.Fatalf("大响应应被压缩: Content-Encoding=%q", compressed.headers.Get("Content-Encoding"))
	}
	if len(compressed.raw) >= len(plain.raw) {
		t.Fatalf("压缩后体积应明显变小: compressed=%d plain=%d", len(compressed.raw), len(plain.raw))
	}
	if got := gzipBody(t, compressed); got != plain.raw {
		t.Fatalf("解压后的内容与未压缩响应不一致")
	}
}

// TestSmallAPIResponsesAreNotGzipped 小于阈值的响应不压缩，避免负收益。
func TestSmallAPIResponsesAreNotGzipped(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "reader", "ReaderPass123", store.RoleReadonly, false)
	cookie, csrf := env.login(t, "reader", "ReaderPass123")

	res := env.do(t, http.MethodGet, "/api/v1/stats/overview", nil,
		withCookie(cookie), withCSRF(csrf),
		func(req *http.Request) { req.Header.Set("Accept-Encoding", "gzip") })
	if res.headers.Get("Content-Encoding") == "gzip" {
		t.Fatalf("小响应不应压缩（空库的 overview 应远小于阈值）: body=%d", len(res.raw))
	}
	if res.status != http.StatusOK {
		t.Fatalf("请求失败: %d", res.status)
	}
}

// TestGzipPreservesRecoveredErrorBody 验证外层 Recover 的错误体不会进入
// 已经收尾的压缩缓冲；普通请求与支持 gzip 的请求均返回结构化错误。
func TestGzipPreservesRecoveredErrorBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, encoding := range []string{"", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			router := gin.New()
			router.Use(auth.RequestID(), auth.Recover(slog.New(slog.NewTextHandler(io.Discard, nil))))
			router.GET("/probe", gzipResponses(), func(*gin.Context) { panic("测试异常") })
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.Header.Set("Accept-Encoding", encoding)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			body := recorder.Body.String()
			if recorder.Code != http.StatusInternalServerError || !strings.Contains(body, "internal_error") || !strings.Contains(body, recorder.Header().Get(auth.HeaderRequestID)) {
				t.Fatalf("应返回带请求 ID 的结构化 500，实际 status=%d body=%q", recorder.Code, body)
			}
		})
	}
}

// TestGzipNegotiatesQualityValues 验证 q=0 和显式声明的优先级，
// 同时检查允许压缩时的响应仍可完整解压。
func TestGzipNegotiatesQualityValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		encoding string
		gzip     bool
	}{
		{"", false},
		{"br, deflate", false},
		{"gzip", true},
		{"GZIP; Q=0.5", true},
		{"gzip;q=0.001", true},
		{"gzip;q=0", false},
		{"gzip;q=0.000", false},
		{"*;q=0", false},
		{"*;q=0.5", true},
		{"gzip;q=0, *;q=1", false},
		{"*;q=1, gzip;q=0", false},
		{"gzip;q=1, *;q=0", true},
		{"gzip;q=invalid", false},
		{"gzip;q=2", false},
		{"gzip;q=-1", false},
		{"gzip;q=NaN", false},
	}
	payload := strings.Repeat("响应内容", 300)
	for _, tc := range cases {
		t.Run(tc.encoding, func(t *testing.T) {
			router := gin.New()
			router.GET("/probe", gzipResponses(), func(c *gin.Context) { c.String(http.StatusOK, payload) })
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.Header.Set("Accept-Encoding", tc.encoding)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if got := recorder.Header().Get("Content-Encoding") == "gzip"; got != tc.gzip {
				t.Fatalf("Accept-Encoding=%q 时 gzip=%v，期望 %v", tc.encoding, got, tc.gzip)
			}
			res := response{headers: recorder.Header(), raw: recorder.Body.String()}
			if got := gzipBody(t, res); got != payload {
				t.Fatal("编码协商后的响应内容不完整")
			}
		})
	}
}
