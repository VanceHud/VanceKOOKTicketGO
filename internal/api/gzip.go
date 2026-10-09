package api

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// gzipMinLength 是启用压缩的最小响应长度：更小的响应压缩收益低于 CPU 开销。
const gzipMinLength = 1024

var gzipWriterPool = sync.Pool{
	New: func() any { return gzip.NewWriter(nil) },
}

// gzipResponses 为支持 gzip 的客户端压缩 API 响应。
//
// 背景：/api/ 的响应（尤其是导出，上限 2 万条消息且含卡片 JSON）此前完全没有压缩，
// 明文传输可达数十 MB。实现要点：
//   - SSE（text/event-stream）必须旁路：事件流不能被缓冲，否则实时推送失效；
//   - 小于阈值的响应不压缩，避免为几百字节付出压缩开销；
//   - 超过阈值后切换为流式压缩，不把大导出整个缓冲进内存；
//   - 无论客户端是否支持 gzip 都带 Vary: Accept-Encoding，共享缓存不会存错变体。
func gzipResponses() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(c.Request.Header.Get("Accept-Encoding")) {
			c.Next()
			return
		}
		w := &gzipResponseWriter{ResponseWriter: c.Writer}
		c.Writer = w
		defer func() {
			// 外层 Recover 在 panic 展开后才写错误响应；此时必须已恢复底层
			// writer，否则错误体会进入已经结束生命周期的压缩缓冲而丢失。
			c.Writer = w.ResponseWriter
			w.finish()
		}()
		c.Next()
	}
}

// acceptsGzip 按质量值判断是否接受 gzip：q=0 表示拒绝，
// 显式的 gzip 声明优先于通配符，不因 *;q=1 覆盖 gzip;q=0。
func acceptsGzip(header string) bool {
	var gzipFound bool
	var gzipQuality, wildcardQuality float64
	for _, part := range strings.Split(header, ",") {
		parts := strings.Split(part, ";")
		encoding := strings.TrimSpace(parts[0])
		isGzip := strings.EqualFold(encoding, "gzip")
		if !isGzip && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(parameter, "=")
			if !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if !ok || err != nil || !(q >= 0 && q <= 1) {
				quality = 0
			} else {
				quality = q
			}
			break
		}
		if isGzip {
			gzipFound = true
			gzipQuality = quality
		} else {
			wildcardQuality = quality
		}
	}
	if gzipFound {
		return gzipQuality > 0
	}
	return wildcardQuality > 0
}

// gzipResponseWriter 延迟决定是否压缩：先缓冲到 gzipMinLength，
// 超过阈值后设置响应头并切换为流式 gzip；SSE 与已带编码的响应直接旁路。
type gzipResponseWriter struct {
	gin.ResponseWriter
	buf     []byte
	started bool
	bypass  bool
	gz      *gzip.Writer
}

func (w *gzipResponseWriter) Write(data []byte) (int, error) {
	if w.bypass {
		return w.ResponseWriter.Write(data)
	}
	if w.started {
		return w.gz.Write(data)
	}
	// SSE 在首个事件前设置 Content-Type，这里据此旁路。
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.bypass = true
		return w.ResponseWriter.Write(data)
	}
	w.buf = append(w.buf, data...)
	if len(w.buf) >= gzipMinLength {
		if err := w.start(); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

func (w *gzipResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// start 切换到 gzip 输出。必须在第一次向底层写入之前调用（响应头此时才落盘）。
func (w *gzipResponseWriter) start() error {
	header := w.Header()
	if header.Get("Content-Encoding") != "" {
		// 已有编码（理论上不会发生）：放弃压缩，原样输出。
		w.bypass = true
		_, err := w.ResponseWriter.Write(w.buf)
		w.buf = nil
		return err
	}
	header.Del("Content-Length")
	header.Set("Content-Encoding", "gzip")
	w.gz = gzipWriterPool.Get().(*gzip.Writer)
	w.gz.Reset(w.ResponseWriter)
	w.started = true
	if _, err := w.gz.Write(w.buf); err != nil {
		return err
	}
	w.buf = nil
	return nil
}

func (w *gzipResponseWriter) Flush() {
	if w.bypass {
		w.ResponseWriter.Flush()
		return
	}
	if w.started {
		_ = w.gz.Flush()
		w.ResponseWriter.Flush()
		return
	}
	// 未达阈值就被 flush：把缓冲原样写出，此后不能再压缩（半压缩流无法拼接）。
	if len(w.buf) > 0 {
		w.bypass = true
		_, _ = w.ResponseWriter.Write(w.buf)
		w.buf = nil
	}
	w.ResponseWriter.Flush()
}

// finish 在 handler 返回后收尾：压缩流补齐尾部校验，未达阈值的缓冲原样写出。
func (w *gzipResponseWriter) finish() {
	if w.gz != nil {
		_ = w.gz.Close()
		gzipWriterPool.Put(w.gz)
		w.gz = nil
		return
	}
	if w.bypass || w.started || len(w.buf) == 0 {
		return
	}
	w.bypass = true
	_, _ = w.ResponseWriter.Write(w.buf)
	w.buf = nil
}

// Unwrap 供 http.ResponseController 逐层透传写超时等控制操作（SSE 依赖）。
func (w *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
