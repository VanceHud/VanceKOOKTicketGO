package kook

import (
	"encoding/json"
	"testing"
)

// TestEventUnmarshalRealMessageShape 是回归测试：
//
// 真实平台的普通消息事件里，extra.type 是数字、用户对象放在 extra.author，
// 且没有顶层 author。早期实现把 extra.type 声明为 string，导致整条事件
// 在 json.Unmarshal 时失败并被网关丢弃 —— 聊天记录因此完全没有入库。
func TestEventUnmarshalRealMessageShape(t *testing.T) {
	raw := []byte(`{
		"channel_type": "GROUP",
		"type": 1,
		"target_id": "3563183185300691",
		"author_id": "3606071103",
		"content": "充值没有到账",
		"msg_id": "67637d4c-0000-0000-0000-000000000000",
		"msg_timestamp": 1607674740160,
		"nonce": "",
		"extra": {
			"type": 1,
			"guild_id": "5000",
			"channel_name": "文字频道",
			"mention": [],
			"mention_all": false,
			"mention_roles": [],
			"mention_here": false,
			"code": "",
			"author": {
				"identify_num": "1234",
				"avatar": "https://img.kaiheila.cn/avatars/a.jpg/icon",
				"username": "asker",
				"id": "3606071103",
				"nickname": "提问用户",
				"roles": []
			},
			"channel_type": 1
		}
	}`)

	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("解析真实消息事件失败: %v", err)
	}

	if !event.IsTextMessage() {
		t.Fatalf("应识别为文本消息，type=%d", event.Type)
	}
	if event.Content != "充值没有到账" {
		t.Fatalf("内容解析异常: %q", event.Content)
	}
	if event.Extra.Type != "1" {
		t.Fatalf("extra.type 应兼容数字形态，得到 %q", event.Extra.Type)
	}
	if event.Extra.GuildID != "5000" {
		t.Fatalf("guild_id 解析异常: %q", event.Extra.GuildID)
	}
	// 顶层没有 author，必须回退到 extra.author，否则归档昵称为空。
	if event.Author.ID != "3606071103" || event.Author.FullName() != "提问用户#1234" {
		t.Fatalf("author 回退解析异常: %+v", event.Author)
	}
}

// TestEventUnmarshalSystemShape 确认系统事件的字符串 extra.type 仍可解析。
func TestEventUnmarshalSystemShape(t *testing.T) {
	raw := []byte(`{
		"channel_type": "GROUP",
		"type": 255,
		"target_id": "chan-1",
		"extra": {
			"type": "message_btn_click",
			"guild_id": "5000",
			"body": {"value": "v", "user_id": "u1", "target_id": "chan-1"}
		}
	}`)

	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("解析系统事件失败: %v", err)
	}
	if !event.IsSystem() {
		t.Fatalf("应识别为系统事件，type=%d", event.Type)
	}
	if event.Extra.Type != ExtraType(SystemEventButtonClick) {
		t.Fatalf("extra.type 解析异常: %q", event.Extra.Type)
	}
}

// TestExtraTypeUnmarshalTolerant 校验 extra.type 的容错：null、非法值时不应报错。
func TestExtraTypeUnmarshalTolerant(t *testing.T) {
	cases := map[string]ExtraType{
		`null`:    "",
		`"abc"`:   "abc",
		`10`:      "10",
		`{}`:      "{}",
		`[1,2]`:   "[1,2]",
		`"emoji"`: "emoji",
	}
	for input, want := range cases {
		var got ExtraType
		if err := json.Unmarshal([]byte(input), &got); err != nil {
			t.Fatalf("解析 %s 失败: %v", input, err)
		}
		if got != want {
			t.Fatalf("解析 %s：期望 %q，得到 %q", input, want, got)
		}
	}
}

// TestEventUnmarshalPrivateMessage 是回归测试：
//
// 私聊消息事件的 extra.type 同样是数字（1/9），用户对象只出现在 extra.author。
// 早期实现把 extra.type 声明为 string，导致所有私聊事件解析失败被网关丢弃，
// 表现为「私聊机器人发 /login、/bind 完全没有反应」。
func TestEventUnmarshalPrivateMessage(t *testing.T) {
	raw := []byte(`{
		"channel_type": "PERSON",
		"type": 9,
		"target_id": "77777",
		"author_id": "3606071103",
		"content": "/login",
		"msg_id": "cb11d28-0000-0000-0000-5700aa4c1b58",
		"msg_timestamp": 1612778254192,
		"nonce": "",
		"extra": {
			"type": 9,
			"author": {
				"identify_num": "1234",
				"username": "staff",
				"id": "3606071103",
				"nickname": "客服小助手",
				"roles": [1002]
			}
		}
	}`)

	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("解析真实私聊事件失败: %v", err)
	}
	if !event.IsDirect() {
		t.Fatalf("应识别为私聊，channel_type=%q", event.ChannelType)
	}
	if !event.IsTextMessage() {
		t.Fatalf("应识别为文本类消息，type=%d", event.Type)
	}
	if event.Content != "/login" {
		t.Fatalf("内容解析异常: %q", event.Content)
	}
	if event.Extra.Type != "9" {
		t.Fatalf("extra.type 应兼容数字形态，得到 %q", event.Extra.Type)
	}
	if event.AuthorID != "3606071103" || event.Author.FullName() != "客服小助手#1234" {
		t.Fatalf("私聊作者解析异常: id=%q author=%+v", event.AuthorID, event.Author)
	}
}

// TestEventUnmarshalPrivateMessageWithoutAuthorID 校验 author_id 缺失时
// 能用 extra.author.id 回填，否则命令分发拿不到发送者。
func TestEventUnmarshalPrivateMessageWithoutAuthorID(t *testing.T) {
	raw := []byte(`{
		"channel_type": "PERSON",
		"type": 1,
		"content": "/tkhelp",
		"extra": {"type": 1, "author": {"id": "42", "username": "u"}}
	}`)

	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if event.AuthorID != "42" {
		t.Fatalf("author_id 应回填为 42，实际 %q", event.AuthorID)
	}
}
