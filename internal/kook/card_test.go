package kook

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAttachmentsUnmarshalShapes 是回归测试：
//
// 平台把附件下发成对象（消息事件 / message/view），历史接口里也出现过数组形态。
// 早期实现完全没有解析 attachments 字段，导致图片消息在 content 为空时
// 归档不到任何地址。这里保证两种形态都能解析。
func TestAttachmentsUnmarshalShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"object", `{"type":"image","name":"a.png","url":"https://img.example/a.png"}`, 1},
		{"array", `[{"type":"file","name":"b.txt","url":"https://files.example/b.txt"}]`, 1},
		{"null", `null`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attachments Attachments
			if err := json.Unmarshal([]byte(tc.raw), &attachments); err != nil {
				t.Fatalf("解析附件失败: %v", err)
			}
			if len(attachments) != tc.want {
				t.Fatalf("附件数量应为 %d，实际 %d", tc.want, len(attachments))
			}
			if tc.want > 0 && attachments.Primary().URL == "" {
				t.Fatalf("Primary 应返回带地址的附件: %+v", attachments)
			}
		})
	}
}

// TestEventCarriesAttachments 验证图片事件里的 extra.attachments 会被解析出来。
func TestEventCarriesAttachments(t *testing.T) {
	raw := []byte(`{
		"channel_type": "GROUP",
		"type": 2,
		"target_id": "chan-1",
		"author_id": "9001",
		"content": "https://img.example/a.png",
		"msg_id": "msg-img-1",
		"extra": {
			"type": 2,
			"guild_id": "5000",
			"attachments": {
				"type": "image",
				"name": "a.png",
				"url": "https://img.example/a.png",
				"file_type": "image/png",
				"size": 1024
			},
			"author": {"id": "9001", "username": "asker"}
		}
	}`)

	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("解析图片事件失败: %v", err)
	}
	attachment := event.Extra.Attachments.Primary()
	if attachment.URL != "https://img.example/a.png" || attachment.Name != "a.png" || attachment.FileType != "image/png" {
		t.Fatalf("附件解析异常: %+v", attachment)
	}
}

// TestCardSummaryAndAttachments 验证卡片 JSON 的摘要提取与附件提取。
func TestCardSummaryAndAttachments(t *testing.T) {
	cardJSON := `[{
		"type": "card",
		"theme": "info",
		"modules": [
			{"type": "header", "text": {"type": "plain-text", "content": "服务器维护公告"}},
			{"type": "section", "text": {"type": "kmarkdown", "content": "维护时间 **22:00**"}},
			{"type": "context", "elements": [
				{"type": "plain-text", "content": "维护期间无法开单"},
				{"type": "image", "src": "https://img.example/icon.png"}
			]},
			{"type": "action-group", "elements": [
				{"type": "button", "text": {"type": "plain-text", "content": "我知道了"}, "click": "return-val", "value": "ok"}
			]},
			{"type": "divider"},
			{"type": "file", "src": "https://files.example/guide.pdf", "title": "guide.pdf"},
			{"type": "video", "src": "https://files.example/demo.mp4", "title": "demo.mp4"}
		]
	}]`

	summary := CardSummary(cardJSON)
	for _, want := range []string{"服务器维护公告", "维护时间 **22:00**", "维护期间无法开单", "我知道了", "[文件] guide.pdf", "[视频] demo.mp4"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("卡片摘要缺少 %q: %q", want, summary)
		}
	}

	attachments := CardAttachments(cardJSON)
	if len(attachments) != 2 {
		t.Fatalf("卡片附件数量应为 2，实际 %d: %+v", len(attachments), attachments)
	}
	if attachments[0].Type != "file" || attachments[0].URL != "https://files.example/guide.pdf" || attachments[0].Name != "guide.pdf" {
		t.Fatalf("文件模块提取异常: %+v", attachments[0])
	}
	if attachments[1].Type != "video" || attachments[1].URL != "https://files.example/demo.mp4" {
		t.Fatalf("视频模块提取异常: %+v", attachments[1])
	}

	// 单个卡片对象（非数组）也要兼容。
	single, err := ParseCards(`{"type":"card","modules":[{"type":"header","text":{"type":"plain-text","content":"标题"}}]}`)
	if err != nil || len(single) != 1 || CardSummary(`{"type":"card","modules":[]}`) != "" {
		t.Fatalf("单个卡片对象解析异常: cards=%+v err=%v", single, err)
	}

	// 非 JSON 内容不应 panic，摘要为空。
	if summary := CardSummary("不是 JSON"); summary != "" {
		t.Fatalf("非法卡片内容应返回空摘要，实际 %q", summary)
	}
	if _, err := ParseCards(""); err == nil {
		t.Fatalf("空内容应返回错误")
	}
}
