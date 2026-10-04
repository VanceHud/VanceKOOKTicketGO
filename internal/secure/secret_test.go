package secure

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := []byte("test-secret-key-for-aes-gcm-0001")
	plaintext := "kook-bot-token-abcdefghijklmnop"

	encoded, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if strings.Contains(encoded, plaintext) {
		t.Fatal("密文中不应包含明文")
	}

	decoded, err := Decrypt(key, encoded)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if decoded != plaintext {
		t.Fatalf("解密结果不一致: %q", decoded)
	}
}

func TestEncryptProducesUniqueCiphertexts(t *testing.T) {
	key := []byte("test-secret-key-for-aes-gcm-0001")
	first, err := Encrypt(key, "same-plaintext")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	second, err := Encrypt(key, "same-plaintext")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	// 每次使用随机 nonce，相同明文应得到不同密文
	if first == second {
		t.Fatal("相同明文的两次加密结果相同，nonce 可能未随机化")
	}
}

func TestDecryptFailsWithWrongKeyOrTamperedData(t *testing.T) {
	key := []byte("test-secret-key-for-aes-gcm-0001")
	encoded, err := Encrypt(key, "sensitive")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	if _, err := Decrypt([]byte("another-secret-key-000000000000"), encoded); err == nil {
		t.Fatal("使用错误密钥解密应当失败")
	}

	// 篡改密文：把中间一个字符换成其它合法 base64 字符（保证确实发生变化）
	middle := len(encoded) / 2
	replacement := byte('A')
	if encoded[middle] == 'A' {
		replacement = 'B'
	}
	tampered := encoded[:middle] + string(replacement) + encoded[middle+1:]
	if tampered == encoded {
		t.Fatal("篡改后的密文与原文相同，测试无效")
	}
	if _, err := Decrypt(key, tampered); err == nil {
		t.Fatal("篡改密文后解密应当失败")
	}

	if _, err := Decrypt(key, "not-base64!!!"); err == nil {
		t.Fatal("非法 base64 应当失败")
	}
}

func TestHashTokenIsStableAndIrreversible(t *testing.T) {
	token := "3f2b9c1d4e5f60718293a4b5c6d7e8f9"
	hash := HashToken(token)

	if hash == token {
		t.Fatal("哈希不应等于原文")
	}
	if strings.Contains(hash, token) {
		t.Fatal("哈希中不应包含原文")
	}
	if HashToken(token) != hash {
		t.Fatal("相同输入应得到相同哈希")
	}
	if len(hash) != 64 {
		t.Fatalf("SHA-256 十六进制长度应为 64，得到 %d", len(hash))
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("abc", "abc") {
		t.Fatal("相同字符串应相等")
	}
	if ConstantTimeEqual("abc", "abd") {
		t.Fatal("不同字符串不应相等")
	}
	if ConstantTimeEqual("abc", "abcd") {
		t.Fatal("长度不同的字符串不应相等")
	}
	if ConstantTimeEqual("", "") {
		t.Fatal("空字符串不应视为相等凭据")
	}
}

func TestRandomHelpers(t *testing.T) {
	hex1, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex 失败: %v", err)
	}
	hex2, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex 失败: %v", err)
	}
	if len(hex1) != 32 || hex1 == hex2 {
		t.Fatalf("RandomHex 结果异常: %s / %s", hex1, hex2)
	}

	code, err := RandomCrockford(6)
	if err != nil {
		t.Fatalf("RandomCrockford 失败: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("RandomCrockford 长度异常: %s", code)
	}
	for _, r := range code {
		if !strings.ContainsRune(crockford, r) {
			t.Fatalf("RandomCrockford 出现字符集外字符: %s", code)
		}
		if strings.ContainsAny(string(r), "ILOU") {
			t.Fatalf("Crockford 字符集不应包含易混字符: %s", code)
		}
	}

	digits, err := RandomDigits(6)
	if err != nil {
		t.Fatalf("RandomDigits 失败: %v", err)
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			t.Fatalf("RandomDigits 出现非数字字符: %s", digits)
		}
	}
}

func TestMaskTail(t *testing.T) {
	if got := MaskTail("abcdefgh"); got != "****efgh" {
		t.Fatalf("掩码结果不符: %s", got)
	}
	if got := MaskTail("ab"); got != "****" {
		t.Fatalf("短值掩码结果不符: %s", got)
	}
	if got := MaskTail(""); got != "" {
		t.Fatalf("空值应返回空串，得到 %q", got)
	}
}
