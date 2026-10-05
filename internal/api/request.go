package api

import (
	"bytes"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"

	"vancekookticket/internal/auth"
)

// API 只接受小型 JSON，没有上传接口。完整读取有界请求体，同时覆盖分块请求
// 和合法 JSON 后追加超大数据的情况；在数据库查询及 bcrypt 之前拒绝。
const maxRequestBodyBytes = 64 << 10

func requestBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(status int, code, message string) {
			c.AbortWithStatusJSON(status, gin.H{"error": apiError{
				Code: code, Message: message, RequestID: auth.RequestIDOf(c),
			}})
		}
		if c.Request.ContentLength > maxRequestBodyBytes {
			fail(http.StatusRequestEntityTooLarge, "request_too_large", "请求体不能超过 64 KiB")
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			body := http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
			payload, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil {
				if _, ok := err.(*http.MaxBytesError); ok {
					fail(http.StatusRequestEntityTooLarge, "request_too_large", "请求体不能超过 64 KiB")
				} else {
					fail(http.StatusBadRequest, "invalid_request", "读取请求体失败")
				}
				return
			}
			if len(payload) > 0 {
				mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
				if err != nil || mediaType != "application/json" {
					// 防止跨站表单或 text/plain 提交 JSON 触发登录 CSRF。
					fail(http.StatusUnsupportedMediaType, "unsupported_media_type", "请求体必须使用 application/json")
					return
				}
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(payload))
		}
		c.Next()
	}
}
