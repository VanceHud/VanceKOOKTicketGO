// Package web 内嵌前端构建产物（web/dist）并提供 SPA 静态服务。
//
// 开发约定：
//   - 仓库中只提交 web/dist/.gitkeep 占位文件，因此**未执行前端构建也能 go build**；
//   - 生产镜像由 Dockerfile 的多阶段构建生成真实 dist 后编译进二进制；
//   - 本地开发前端时使用 vite dev server，并通过代理访问后端接口。
package web

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

// gzipMinSize 是启用压缩的最小体积：小文件省下的字节不值得额外的 CPU 与头部开销。
const gzipMinSize = 1024

// assetPayload 是静态资源的可复用负载（压缩结果按文件名缓存）。
type assetPayload struct {
	data []byte
	gzip bool
}

// Handler 提供静态资源与 SPA 兜底路由。
type Handler struct {
	fsys     fs.FS
	index    []byte
	hasIndex bool

	// 构建产物带内容哈希且不可变，gzip 结果按文件名缓存，
	// 每个资源只在首次被请求时压缩一次。
	gzipMu     sync.Mutex
	gzipAssets map[string]assetPayload
}

// New 创建静态资源处理器。
func New() (*Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	h := &Handler{fsys: sub, gzipAssets: map[string]assetPayload{}}
	if data, err := fs.ReadFile(sub, "index.html"); err == nil {
		h.index = data
		h.hasIndex = true
	}
	return h, nil
}

// HasIndex 报告是否已内嵌真实前端产物。
func (h *Handler) HasIndex() bool { return h.hasIndex }

// Serve 处理未匹配到路由的请求：先找静态资源，找不到则回退到 index.html。
func (h *Handler) Serve(c *gin.Context) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		abortNotFound(c)
		return
	}
	// 未注册的 /api 路径必须返回 JSON 404，不能回落到前端页面。
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		abortNotFound(c)
		return
	}

	// 先拼成绝对路径再 Clean，消除 ".." 造成的目录穿越。
	name := strings.TrimPrefix(path.Clean("/"+c.Request.URL.Path), "/")
	if name != "" {
		if file, err := h.fsys.Open(name); err == nil {
			defer func() { _ = file.Close() }()
			if info, err := file.Stat(); err == nil && !info.IsDir() {
				h.serveFile(c, name, file, info)
				return
			}
		}
	}
	h.serveIndex(c)
}

func (h *Handler) serveFile(c *gin.Context, name string, file fs.File, info fs.FileInfo) {
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")

	// 带内容哈希的构建产物可以长期缓存；其余（index.html、favicon 等）不缓存。
	// 可压缩资源一律带 Vary: Accept-Encoding：共享缓存若先存了未压缩变体，
	// 之后会把它发给支持 gzip 的客户端（压缩分支在下方设置，这里覆盖其余路径）。
	if strings.HasPrefix(name, "assets/") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		if compressibleType(contentType) {
			c.Header("Vary", "Accept-Encoding")
		}
	} else {
		c.Header("Cache-Control", "no-store")
	}

	// 只有「可能压缩」的请求才整体读入：首屏入口 JS 接近 400KB，
	// 压缩后传输体积约为 1/3；其余请求走 ServeContent 的流式路径，
	// 不为每个静态请求多做一次整文件分配。
	if acceptsGzip(c.Request) && compressibleType(contentType) && info.Size() >= gzipMinSize {
		data, err := io.ReadAll(file)
		if err != nil {
			abortNotFound(c)
			return
		}
		if payload, ok := h.gzipPayload(name, data); ok {
			c.Header("Content-Encoding", "gzip")
			c.Header("Vary", "Accept-Encoding")
			c.Header("Content-Length", strconv.Itoa(len(payload)))
			c.Writer.WriteHeader(http.StatusOK)
			if c.Request.Method != http.MethodHead {
				_, _ = c.Writer.Write(payload)
			}
			return
		}
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), bytes.NewReader(data))
		return
	}

	if seeker, ok := file.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), seeker)
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		abortNotFound(c)
		return
	}
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), bytes.NewReader(data))
}

// gzipPayload 返回可 gzip 传输的负载（按文件名缓存压缩结果）。
//
// 压缩没有收益时（例如已被压缩的格式）返回 ok=false，调用方退回未压缩响应。
func (h *Handler) gzipPayload(name string, data []byte) ([]byte, bool) {
	h.gzipMu.Lock()
	defer h.gzipMu.Unlock()
	entry, ok := h.gzipAssets[name]
	if !ok {
		if compressed, err := gzipBytes(data); err == nil && len(compressed) < len(data) {
			entry = assetPayload{data: compressed, gzip: true}
		} else {
			entry = assetPayload{data: data}
		}
		h.gzipAssets[name] = entry
	}
	return entry.data, entry.gzip
}

// acceptsGzip 判断客户端是否接受 gzip 编码。
func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// compressibleType 判断内容类型是否适合 gzip（文本类都值得压）。
func compressibleType(contentType string) bool {
	ct := contentType
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/javascript", "application/json", "application/xml",
		"application/manifest+json", "image/svg+xml":
		return true
	default:
		return false
	}
}

// gzipBytes 压缩数据。
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// serveIndex 返回 SPA 入口；
// 若前端尚未构建，则返回一段引导信息，方便后端开发者快速定位。
func (h *Handler) serveIndex(c *gin.Context) {
	if !h.hasIndex {
		c.Header("Cache-Control", "no-store")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusServiceUnavailable, notBuiltPage)
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(h.index)
}

func abortNotFound(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
		"error": gin.H{"code": "not_found", "message": "资源不存在"},
	})
}

const notBuiltPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>前端尚未构建</title>
<style>body{font-family:system-ui,-apple-system,'Segoe UI',sans-serif;max-width:720px;margin:64px auto;line-height:1.7;color:#111}
code{background:#f4f4f5;padding:2px 6px;border-radius:4px}pre{background:#f4f4f5;padding:12px;border-radius:8px;overflow:auto}</style>
</head><body>
<h1>前端资源尚未构建</h1>
<p>后端已正常启动，但二进制中没有内嵌前端产物。执行以下命令后重新启动：</p>
<pre>cd web/frontend
npm ci
npm run build
cd ../..
go build ./cmd/server</pre>
<p>开发前端时也可以直接运行 <code>npm run dev</code>，由 Vite 代理 <code>/api</code> 到本服务。</p>
</body></html>`
