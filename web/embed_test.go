package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// newTestHandler 构造一个以内嵌资源为数据源的处理器（绕过 embed，便于构造大文件）。
func newTestHandler(files fstest.MapFS) *Handler {
	return &Handler{fsys: files, gzipAssets: map[string]assetPayload{}}
}

func serveAsset(t *testing.T, h *Handler, target string, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if acceptEncoding != "" {
		c.Request.Header.Set("Accept-Encoding", acceptEncoding)
	}
	h.Serve(c)
	return rec
}

// TestServeFileGzipsCompressibleAssets 验证大体积 JS/CSS 在客户端支持时以
// gzip 传输（首屏入口 chunk 近 400KB），并且解压后内容与原始文件一致。
func TestServeFileGzipsCompressibleAssets(t *testing.T) {
	body := []byte(strings.Repeat("export function handler(){ return 1 }\n", 400))
	files := fstest.MapFS{
		"index.html":            {Data: []byte("<html></html>")},
		"assets/index-abc.js":   {Data: body},
		"assets/logo-small.svg": {Data: []byte("<svg/>")},
	}
	h := newTestHandler(files)

	rec := serveAsset(t, h, "/assets/index-abc.js", "gzip, deflate, br")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("大体积 JS 应以 gzip 传输，Content-Encoding=%q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("压缩响应必须声明 Vary: Accept-Encoding，实际 %q", got)
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("响应不是合法的 gzip 数据: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatal("解压后的内容与原始文件不一致")
	}
	if len(rec.Body.Bytes()) >= len(body) {
		t.Fatalf("压缩后体积应更小：%d → %d", len(body), len(rec.Body.Bytes()))
	}

	// 不支持 gzip 的客户端得到原文。
	plain := serveAsset(t, h, "/assets/index-abc.js", "")
	if got := plain.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("未声明 Accept-Encoding 时不应压缩，Content-Encoding=%q", got)
	}
	if !bytes.Equal(plain.Body.Bytes(), body) {
		t.Fatal("未压缩响应内容与原始文件不一致")
	}

	// 小文件不压缩（收益低于开销）。
	small := serveAsset(t, h, "/assets/logo-small.svg", "gzip")
	if got := small.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("小文件不应压缩，Content-Encoding=%q", got)
	}
}

// TestServeFileCachesCompressedPayload 验证压缩结果按文件名缓存：
// 重复请求不应反复压缩，且两次响应完全一致。
func TestServeFileCachesCompressedPayload(t *testing.T) {
	body := []byte(strings.Repeat("const value = 42;\n", 300))
	h := newTestHandler(fstest.MapFS{"assets/app.js": {Data: body}})

	first := serveAsset(t, h, "/assets/app.js", "gzip")
	second := serveAsset(t, h, "/assets/app.js", "gzip")
	if first.Header().Get("Content-Encoding") != "gzip" || second.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("两次请求都应返回 gzip 响应")
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("缓存前后两次响应应完全一致")
	}
	if _, ok := h.gzipAssets["assets/app.js"]; !ok {
		t.Fatal("压缩结果应被缓存")
	}
}
