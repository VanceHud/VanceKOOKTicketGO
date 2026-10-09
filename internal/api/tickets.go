package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/VanceHud/VanceKOOKTicketGO/internal/auth"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/store"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/ticket"
	"github.com/VanceHud/VanceKOOKTicketGO/internal/ticketno"
)

// 备注与消息分页的长度上限，避免超长文本破坏界面与日志。
const (
	maxNoteLength   = 2000
	maxMessageLimit = 2000
	defaultMsgLimit = 500
	// maxMessageOffset 是消息 offset 分页的上限：SQLite 的 OFFSET 逐行步进，
	// 不设上限时一个超大 offset 就能让查询空转。
	maxMessageOffset = 100000
)

// ticketActor 依据当前身份构造业务操作者（含审计所需的 IP 与请求 ID）。
func (s *Server) ticketActor(c *gin.Context) ticket.Actor {
	actor := ticket.Actor{
		Source:    "web",
		IP:        auth.ClientIPOf(c),
		RequestID: auth.RequestIDOf(c),
	}
	if identity := auth.IdentityOf(c); identity != nil {
		actor.ID = identity.Username
		actor.Role = identity.Role
		actor.Name = identity.DisplayName
		if actor.Name == "" {
			actor.Name = identity.Username
		}
	}
	return actor
}

// validTicketStatus 校验状态取值。
//
// 只接受真实会出现的状态：失败的建频道流程会回收编号（记录被删除），
// 不存在 failed 状态的工单。
func validTicketStatus(status string) bool {
	switch status {
	case store.TicketPending, store.TicketOpen, store.TicketLocked, store.TicketClosed:
		return true
	default:
		return false
	}
}

// handleTicketList 分页查询工单。
func (s *Server) handleTicketList(c *gin.Context) {
	page, size := pageParams(c)
	filter := store.TicketFilter{
		Page:     page,
		PageSize: size,
		Query:    c.Query("q"),
	}

	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !validTicketStatus(part) {
				s.fail(c, http.StatusBadRequest, "invalid_request", "工单状态取值不合法")
				return
			}
			filter.Statuses = append(filter.Statuses, part)
		}
	}

	if raw := strings.TrimSpace(c.Query("typeId")); raw != "" {
		typeID := atoiDefault(raw, 0)
		if typeID <= 0 {
			s.fail(c, http.StatusBadRequest, "invalid_request", "工单类型参数不合法")
			return
		}
		value := uint(typeID)
		filter.TypeID = &value
	}

	from, err := timeParam(c, "from", s.Config.Location, false)
	if err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	to, err := timeParam(c, "to", s.Config.Location, true)
	if err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	filter.From, filter.To = from, to

	items, total, err := s.Store.Tickets.List(filter)
	if err != nil {
		s.failInternal(c, err, "ticket.list")
		return
	}
	if items == nil {
		items = []store.Ticket{}
	}
	c.JSON(http.StatusOK, gin.H{
		"items":    items,
		"total":    total,
		"page":     page,
		"pageSize": size,
	})
}

// lookupTicket 按编号取工单；编号格式非法时与不存在同样处理，避免泄露编号规则。
func (s *Server) lookupTicket(c *gin.Context) (*store.Ticket, bool) {
	no := strings.TrimSpace(c.Param("no"))
	if !ticketno.Valid(no) {
		s.fail(c, http.StatusNotFound, "not_found", "工单不存在")
		return nil, false
	}
	t, err := s.Store.Tickets.ByNo(no)
	if err != nil {
		s.failStore(c, err, "ticket.lookup")
		return nil, false
	}
	return t, true
}

// handleTicketDetail 返回工单详情。
func (s *Server) handleTicketDetail(c *gin.Context) {
	t, ok := s.lookupTicket(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, t)
}

// handleTicketMessages 返回工单聊天记录。
//
// tail=1 返回最新的一段（前端默认），否则按 offset 正序分页；
// 响应带 total，前端据此提示“仅显示最近 N 条”。
func (s *Server) handleTicketMessages(c *gin.Context) {
	if _, ok := s.lookupTicket(c); !ok {
		return
	}
	limit := atoiDefault(c.Query("limit"), defaultMsgLimit)
	if limit <= 0 || limit > maxMessageLimit {
		limit = defaultMsgLimit
	}
	offset := atoiDefault(c.Query("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	if offset > maxMessageOffset {
		offset = maxMessageOffset
	}
	tail := c.Query("tail") == "1" || strings.EqualFold(c.Query("tail"), "true")

	no := strings.TrimSpace(c.Param("no"))
	var (
		msgs []store.TicketMessage
		err  error
	)
	if tail {
		msgs, err = s.Store.Tickets.MessagesTail(no, limit)
	} else {
		msgs, err = s.Store.Tickets.Messages(no, limit, offset)
	}
	if err != nil {
		s.failInternal(c, err, "ticket.messages")
		return
	}
	// total 只在结果可能被截断时才统计：返回条数不足 limit 说明已到末尾，
	// total 就是已有条数（offset 模式加上偏移），省掉一次 COUNT 索引扫描。
	// 该接口在 SSE 事件驱动下会被反复重拉，COUNT 是热路径上的固定开销。
	total := int64(len(msgs))
	if !tail {
		total += int64(offset)
	}
	if len(msgs) == limit {
		if total, err = s.Store.Tickets.CountMessages(no); err != nil {
			s.failInternal(c, err, "ticket.messages.count")
			return
		}
	}
	if msgs == nil {
		msgs = []store.TicketMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"items": msgs, "limit": limit, "offset": offset, "total": total, "tail": tail})
}

// handleTicketNotes 返回工单备注。
func (s *Server) handleTicketNotes(c *gin.Context) {
	if _, ok := s.lookupTicket(c); !ok {
		return
	}
	notes, err := s.Tickets.Notes(strings.TrimSpace(c.Param("no")))
	if err != nil {
		s.failInternal(c, err, "ticket.notes")
		return
	}
	if notes == nil {
		notes = []store.TicketNote{}
	}
	c.JSON(http.StatusOK, gin.H{"items": notes})
}

type noteRequest struct {
	Content string `json:"content"`
}

// handleTicketAddNote 新增备注（对应原项目 /tkcm）。
// 工单存在性由 service 内的查询统一判定（ErrNotFound → 404），handler 不再预查一次。
func (s *Server) handleTicketAddNote(c *gin.Context) {
	var req noteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		s.fail(c, http.StatusBadRequest, "invalid_request", "备注内容不能为空")
		return
	}
	if len([]rune(content)) > maxNoteLength {
		s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("备注不能超过 %d 个字符", maxNoteLength))
		return
	}

	note, err := s.Tickets.AddNote(strings.TrimSpace(c.Param("no")), s.ticketActor(c), content)
	if err != nil {
		s.failTicketError(c, err, "ticket.add_note")
		return
	}
	c.JSON(http.StatusCreated, note)
}

type closeRequest struct {
	Note string `json:"note"`
}

// handleTicketClose 关闭工单。
// 工单存在性与状态校验都在 service 内完成（ErrNotFound → 404、状态冲突 → 409），
// handler 不再预查一次同一行。
func (s *Server) handleTicketClose(c *gin.Context) {
	var req closeRequest
	// 空请求体（无 body）是有意允许的：此时按照“不带关闭说明”处理。
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > ticket.MaxCloseNoteLen {
		s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("关闭说明不能超过 %d 个字符", ticket.MaxCloseNoteLen))
		return
	}

	updated, err := s.Tickets.Close(c.Request.Context(), strings.TrimSpace(c.Param("no")), s.ticketActor(c), note)
	if err != nil {
		s.failTicketError(c, err, "ticket.close")
		return
	}
	c.JSON(http.StatusOK, updated)
}

type lockRequest struct {
	Reason string `json:"reason"`
}

// handleTicketLock 手动锁定工单；只接受人工锁定原因，超时锁定由后台任务产生。
// 存在性与状态校验由 service 完成，handler 不预查。
func (s *Server) handleTicketLock(c *gin.Context) {
	var req lockRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	if req.Reason != "" && req.Reason != store.LockReasonManual {
		s.fail(c, http.StatusBadRequest, "invalid_request", "锁定原因不支持该取值")
		return
	}

	updated, err := s.Tickets.Lock(c.Request.Context(), strings.TrimSpace(c.Param("no")), s.ticketActor(c), store.LockReasonManual)
	if err != nil {
		s.failTicketError(c, err, "ticket.lock")
		return
	}
	c.JSON(http.StatusOK, updated)
}

// handleTicketReopen 重新激活已锁定的工单。
// 存在性与状态校验由 service 完成，handler 不预查。
func (s *Server) handleTicketReopen(c *gin.Context) {
	updated, err := s.Tickets.Reopen(c.Request.Context(), strings.TrimSpace(c.Param("no")), s.ticketActor(c))
	if err != nil {
		s.failTicketError(c, err, "ticket.reopen")
		return
	}
	c.JSON(http.StatusOK, updated)
}

// failTicketError 把工单业务错误映射为响应。
func (s *Server) failTicketError(c *gin.Context, err error, action string) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ticket.ErrNoPlatform):
		s.fail(c, http.StatusServiceUnavailable, "bot_unavailable", "机器人未连接，请连接后重试")
	case isInvalidState(err):
		s.fail(c, http.StatusConflict, "invalid_state", strings.TrimPrefix(err.Error(), "工单当前状态不允许该操作："))
	default:
		s.failStore(c, err, action)
	}
}

// isInvalidState 判断是否为「状态不允许该操作」的业务错误。
//
// 用 errors.Is 而不是字符串包含：包装链里恰好含相同文案的其它错误
// 不应被误判为状态冲突。
func isInvalidState(err error) bool {
	return errors.Is(err, ticket.ErrInvalidState)
}

// handleTicketExport 导出工单聊天记录，支持 json / csv / html。
func (s *Server) handleTicketExport(c *gin.Context) {
	t, ok := s.lookupTicket(c)
	if !ok {
		return
	}
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "json")))
	if format != "json" && format != "csv" && format != "html" {
		s.fail(c, http.StatusBadRequest, "invalid_request", "format 仅支持 json、csv、html")
		return
	}

	messages, err := s.Store.Tickets.ExportMessages(t.No)
	if err != nil {
		if errors.Is(err, store.ErrExportTooLarge) {
			s.fail(c, http.StatusUnprocessableEntity, "export_limit_exceeded", "工单消息超过 20,000 条，无法一次性导出")
			return
		}
		s.failInternal(c, err, "ticket.export.messages")
		return
	}
	var notes []store.TicketNote
	if format != "csv" {
		notes, err = s.Store.Tickets.Notes(t.No)
		if err != nil {
			s.failInternal(c, err, "ticket.export.notes")
			return
		}
	}

	exporter := actorName(c)

	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("%s.%s", t.No, format)))

	switch format {
	case "json":
		payload := gin.H{
			"exportedAt": store.Now(),
			"exportedBy": exporter,
			"ticket":     t,
			"messages":   messages,
			"notes":      notes,
		}
		// 直接流式编码到响应，避免 gin 的渲染缓冲再复制一份大对象
		// （单条消息的卡片 JSON 可达数十 KB）。
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.Status(http.StatusOK)
		if err := json.NewEncoder(c.Writer).Encode(payload); err != nil {
			s.Log.Error("导出 JSON 失败", "ticket_no", t.No, "err", err)
			return
		}
	case "csv":
		// 同样边写边发：CSV 行之间没有结构依赖，不需要整体缓冲。
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Status(http.StatusOK)
		// BOM 保证 Excel 正确识别 UTF-8。
		if _, err := c.Writer.WriteString("\ufeff"); err != nil {
			s.Log.Error("导出 CSV 失败", "ticket_no", t.No, "err", err)
			return
		}
		writer := csv.NewWriter(c.Writer)
		_ = writer.Write([]string{"时间(UTC)", "消息ID", "用户ID", "用户名", "类型", "来源", "内容", "媒体链接"})
		for _, m := range messages {
			source := "用户"
			if m.IsBot {
				source = "机器人"
			}
			row := []string{
				m.CreatedAt.Format("2006-01-02 15:04:05"),
				m.MsgID,
				m.UserID,
				m.UserName,
				m.MsgType,
				source,
				m.Content,
				mediaURL(m),
			}
			for i := range row {
				row[i] = csvText(row[i])
			}
			_ = writer.Write(row)
		}
		writer.Flush()
		// 响应已开始发送，无法再改状态码，只能记录日志。
		if err := writer.Error(); err != nil {
			s.Log.Error("导出 CSV 失败", "ticket_no", t.No, "err", err)
			return
		}
	case "html":
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Status(http.StatusOK)
		if _, err := c.Writer.WriteString(renderTicketHTML(t, messages, notes, exporter)); err != nil {
			s.Log.Error("导出 HTML 失败", "ticket_no", t.No, "err", err)
			return
		}
	}

	s.audit(c, "ticket.export", t.No, fmt.Sprintf("导出聊天记录（格式：%s，消息数：%d）", format, len(messages)))
}

// csvText 把潜在公式强制作为文本，包括前置空白/控制符绕过。
func csvText(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") ||
		strings.HasPrefix(trimmed, "=") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "@") {
		return "'" + value
	}
	return value
}

// renderTicketHTML 生成可离线查看的记录页。
// 所有动态内容都经 html.EscapeString 转义，避免聊天内容里的标记被当作 HTML 执行。
func renderTicketHTML(t *store.Ticket, messages []store.TicketMessage, notes []store.TicketNote, exporter string) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>工单 " + html.EscapeString(t.No) + " 记录</title>")
	b.WriteString("<style>body{font-family:system-ui,-apple-system,'Segoe UI',sans-serif;margin:24px;line-height:1.6;color:#111}" +
		"h1{font-size:20px}table{border-collapse:collapse;width:100%;margin-top:12px}" +
		"th,td{border:1px solid #ddd;padding:6px 8px;font-size:13px;vertical-align:top;text-align:left}" +
		"th{background:#f6f6f6}time{color:#666;white-space:nowrap}.bot{color:#0b6}.note{background:#fffbe6;padding:8px;border:1px solid #ffe58f}" +
		"</style></head><body>")
	b.WriteString("<h1>工单 " + html.EscapeString(t.No) + "</h1>")
	b.WriteString("<p>开单人：" + html.EscapeString(t.UserName) + "（" + html.EscapeString(t.UserID) + "）<br>")
	if t.TypeName != "" {
		b.WriteString("工单类型：" + html.EscapeString(t.TypeName) + "<br>")
	}
	b.WriteString("状态：" + html.EscapeString(t.Status) + "<br>")
	b.WriteString("开启时间(UTC)：" + t.StartedAt.Format("2006-01-02 15:04:05") + "<br>")
	if t.ClosedAt != nil {
		b.WriteString("关闭时间(UTC)：" + t.ClosedAt.Format("2006-01-02 15:04:05") + "，操作人：" + html.EscapeString(t.ClosedByName) + "<br>")
	}
	// 关闭说明可能多行：先转义再换行转 <br>，保持与 KOOK 通知一致的阅读效果。
	if strings.TrimSpace(t.CloseNote) != "" {
		b.WriteString("关闭说明：" + strings.ReplaceAll(html.EscapeString(t.CloseNote), "\n", "<br>") + "<br>")
	}
	b.WriteString("导出人：" + html.EscapeString(exporter) + "，导出时间(UTC)：" + store.Now().Format("2006-01-02 15:04:05") + "</p>")
	b.WriteString("<h2>聊天记录（" + fmt.Sprint(len(messages)) + " 条）</h2><table><thead><tr><th>时间(UTC)</th><th>发送者</th><th>内容</th></tr></thead><tbody>")
	for _, m := range messages {
		who := html.EscapeString(m.UserName)
		if m.IsBot {
			who += " <span class=\"bot\">[机器人]</span>"
		}
		b.WriteString("<tr><td><time>" + m.CreatedAt.Format("2006-01-02 15:04:05") + "</time></td><td>" + who + "</td><td>" +
			renderHTMLMessage(m) + "</td></tr>")
	}
	b.WriteString("</tbody></table>")
	if len(notes) > 0 {
		b.WriteString("<h2>备注（" + fmt.Sprint(len(notes)) + " 条）</h2>")
		for _, n := range notes {
			b.WriteString("<div class=\"note\"><strong>" + html.EscapeString(n.AuthorName) + "</strong> <time>" +
				n.CreatedAt.Format("2006-01-02 15:04:05") + "</time><br>" + html.EscapeString(n.Content) + "</div>")
		}
	}
	b.WriteString("</body></html>")
	return b.String()
}

// renderHTMLMessage 渲染单条消息：图片/视频/语音内联展示，文件渲染为下载链接。
//
// 所有动态内容（包括资源地址）都经过 html.EscapeString 转义。
func renderHTMLMessage(m store.TicketMessage) string {
	url := mediaURL(m)
	escapedURL := html.EscapeString(url)
	switch m.MsgType {
	case store.MsgTypeImage:
		if url != "" {
			return `<a href="` + escapedURL + `" target="_blank" rel="noopener"><img src="` + escapedURL +
				`" alt="` + html.EscapeString(mediaLabel(m)) + `" style="max-width:360px;max-height:360px;border-radius:6px"></a>`
		}
	case store.MsgTypeVideo:
		if url != "" {
			return `<video controls preload="metadata" src="` + escapedURL + `" style="max-width:360px"></video><br><a href="` +
				escapedURL + `" target="_blank" rel="noopener">打开视频</a>`
		}
	case store.MsgTypeAudio:
		if url != "" {
			return `<audio controls preload="metadata" src="` + escapedURL + `"></audio><br><a href="` + escapedURL +
				`" target="_blank" rel="noopener">` + html.EscapeString(mediaLabel(m)) + `</a>`
		}
	case store.MsgTypeFile:
		if url != "" {
			return `<a href="` + escapedURL + `" download>` + html.EscapeString(mediaLabel(m)) + `</a>`
		}
	case store.MsgTypeCard:
		// 卡片无法在离线 HTML 里还原，展示文本摘要并附上卡片内的文件链接（如果有）。
		if url != "" {
			return html.EscapeString(m.Content) + `<br><a href="` + escapedURL + `" download>` + html.EscapeString(mediaLabel(m)) + `</a>`
		}
	}
	return html.EscapeString(m.Content)
}

// mediaURL 返回富媒体消息的实际地址：优先结构化字段，兼容旧记录的「[类型] 地址」文本。
func mediaURL(m store.TicketMessage) string {
	if raw := strings.TrimSpace(m.MediaURL); raw != "" {
		return safeMediaURL(raw)
	}
	content := strings.TrimSpace(m.Content)
	for _, prefix := range []string{"[图片] ", "[视频] ", "[文件] ", "[语音] "} {
		if strings.HasPrefix(content, prefix) {
			content = strings.TrimSpace(strings.TrimPrefix(content, prefix))
			break
		}
	}
	if fields := strings.Fields(content); len(fields) > 0 {
		return safeMediaURL(fields[0])
	}
	return ""
}

func safeMediaURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}

// mediaLabel 返回资源的展示名称（文件名优先，其次从地址推导）。
func mediaLabel(m store.TicketMessage) string {
	if name := strings.TrimSpace(m.MediaName); name != "" {
		return name
	}
	if url := mediaURL(m); url != "" {
		if index := strings.LastIndex(url, "/"); index >= 0 && index+1 < len(url) {
			return url[index+1:]
		}
		return url
	}
	return "附件"
}
