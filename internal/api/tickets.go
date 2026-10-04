package api

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"vancekookticket/internal/auth"
	"vancekookticket/internal/store"
	"vancekookticket/internal/ticket"
	"vancekookticket/internal/ticketno"
)

// 备注与关闭说明的长度上限，避免超长文本破坏界面与日志。
const (
	maxNoteLength   = 2000
	maxCloseNoteLen = 1000
	maxMessageLimit = 2000
	defaultMsgLimit = 500
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
func validTicketStatus(status string) bool {
	switch status {
	case store.TicketPending, store.TicketOpen, store.TicketLocked, store.TicketClosed, store.TicketFailed:
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

// handleTicketMessages 返回工单聊天记录（分页）。
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
	msgs, err := s.Store.Tickets.Messages(strings.TrimSpace(c.Param("no")), limit, offset)
	if err != nil {
		s.failInternal(c, err, "ticket.messages")
		return
	}
	if msgs == nil {
		msgs = []store.TicketMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"items": msgs, "limit": limit, "offset": offset})
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
func (s *Server) handleTicketAddNote(c *gin.Context) {
	if _, ok := s.lookupTicket(c); !ok {
		return
	}
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
func (s *Server) handleTicketClose(c *gin.Context) {
	if _, ok := s.lookupTicket(c); !ok {
		return
	}
	var req closeRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		s.fail(c, http.StatusBadRequest, "invalid_request", "请求参数不合法")
		return
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > maxCloseNoteLen {
		s.fail(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("关闭说明不能超过 %d 个字符", maxCloseNoteLen))
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
func (s *Server) handleTicketLock(c *gin.Context) {
	if _, ok := s.lookupTicket(c); !ok {
		return
	}
	var req lockRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
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
func (s *Server) handleTicketReopen(c *gin.Context) {
	if _, ok := s.lookupTicket(c); !ok {
		return
	}
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
	case isInvalidState(err):
		s.fail(c, http.StatusConflict, "invalid_state", strings.TrimPrefix(err.Error(), "工单当前状态不允许该操作："))
	default:
		s.failStore(c, err, action)
	}
}

func isInvalidState(err error) bool {
	return err != nil && strings.Contains(err.Error(), ticket.ErrInvalidState.Error())
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

	messages, err := s.Store.Tickets.Messages(t.No, store.MaxExportMessages, 0)
	if err != nil {
		s.failInternal(c, err, "ticket.export.messages")
		return
	}
	notes, err := s.Store.Tickets.Notes(t.No)
	if err != nil {
		s.failInternal(c, err, "ticket.export.notes")
		return
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
		c.JSON(http.StatusOK, payload)
	case "csv":
		var buf bytes.Buffer
		// 加 BOM，保证 Excel 正确识别 UTF-8。
		buf.WriteString("\ufeff")
		writer := csv.NewWriter(&buf)
		_ = writer.Write([]string{"时间(UTC)", "消息ID", "用户ID", "用户名", "类型", "来源", "内容"})
		for _, m := range messages {
			source := "用户"
			if m.IsBot {
				source = "机器人"
			}
			_ = writer.Write([]string{
				m.CreatedAt.Format("2006-01-02 15:04:05"),
				m.MsgID,
				m.UserID,
				m.UserName,
				m.MsgType,
				source,
				m.Content,
			})
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			s.failInternal(c, err, "ticket.export.csv")
			return
		}
		c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
	case "html":
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderTicketHTML(t, messages, notes, exporter)))
	}

	s.audit(c, "ticket.export", t.No, fmt.Sprintf("导出聊天记录（格式：%s，消息数：%d）", format, len(messages)))
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
	b.WriteString("状态：" + html.EscapeString(t.Status) + "<br>")
	b.WriteString("开启时间(UTC)：" + t.StartedAt.Format("2006-01-02 15:04:05") + "<br>")
	if t.ClosedAt != nil {
		b.WriteString("关闭时间(UTC)：" + t.ClosedAt.Format("2006-01-02 15:04:05") + "，操作人：" + html.EscapeString(t.ClosedByName) + "<br>")
	}
	b.WriteString("导出人：" + html.EscapeString(exporter) + "，导出时间(UTC)：" + store.Now().Format("2006-01-02 15:04:05") + "</p>")
	b.WriteString("<h2>聊天记录（" + fmt.Sprint(len(messages)) + " 条）</h2><table><thead><tr><th>时间(UTC)</th><th>发送者</th><th>内容</th></tr></thead><tbody>")
	for _, m := range messages {
		who := html.EscapeString(m.UserName)
		if m.IsBot {
			who += " <span class=\"bot\">[机器人]</span>"
		}
		b.WriteString("<tr><td><time>" + m.CreatedAt.Format("2006-01-02 15:04:05") + "</time></td><td>" + who + "</td><td>" +
			html.EscapeString(m.Content) + "</td></tr>")
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
