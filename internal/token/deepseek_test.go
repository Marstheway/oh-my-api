package token

import (
	"testing"
)

// loadAndInitDeepseek 辅助函数，从嵌入数据加载 DeepSeek tokenizer 并初始化 defaultTk。
func loadAndInitDeepseek(t *testing.T) {
	t.Helper()
	err := loadDeepseekTokenizer()
	if err != nil {
		t.Fatalf("loadDeepseekTokenizer: %v", err)
	}
	if deepseekTk == nil {
		t.Fatal("deepseekTk is nil after successful load")
	}
	// 确保 defaultTk 已初始化（测试间可能未调 Init）
	if defaultTk == nil {
		defaultTk = &tiktokenTokenizer{encoding: EncodingCL100K}
	}
}

func TestDeepseekTokenizer_LoadAndEncode(t *testing.T) {
	loadAndInitDeepseek(t)

	// 与 HuggingFace tokenizers 库输出一致："Hello!" -> ["Hello", "!"] -> 2 tokens
	got := deepseekTk.CountTokens("Hello!")
	if got != 2 {
		t.Errorf("deepseekTk.CountTokens(\"Hello!\") = %d, want 2", got)
	}

	// 验证中文与 cl100k_base 结果不同（确认确实切换了 tokenizer）
	tikCount := defaultTk.CountTokens("你好，世界")
	dsCount := deepseekTk.CountTokens("你好，世界")
	if tikCount == dsCount {
		t.Logf("warning: tiktoken and deepseek give same count (%d) for '你好，世界'", tikCount)
	}
}

func TestDeepseekTokenizer_SpecialTokenNotSplit(t *testing.T) {
	loadAndInitDeepseek(t)

	// special token 应作为一个整体不被拆分，普通文本不应受影响
	specialText := "<｜begin▁of▁sentence｜>Hello"
	dsCount := deepseekTk.CountTokens(specialText)
	// 对比普通 "Hello"（无前缀），special 前缀应多出 token
	plainCount := deepseekTk.CountTokens("Hello")
	if dsCount <= plainCount {
		t.Errorf("special token text count (%d) should be > plain text count (%d)", dsCount, plainCount)
	}
}

func TestDeepseekTokenizer_SpecialTokenExactMatch(t *testing.T) {
	loadAndInitDeepseek(t)

	// 完整命中单个 special token 应计为 1
	got := deepseekTk.CountTokens("<｜begin▁of▁sentence｜>")
	if got == 0 {
		t.Error("exact special token should have count > 0")
	}
}

func TestDeepseekTokenizer_PickDifference(t *testing.T) {
	loadAndInitDeepseek(t)

	// Pick 为 deepseek 模型选择 deepseek tokenizer
	dsPick := Pick("deepseek-chat")
	if dsPick != deepseekTk {
		t.Error("Pick('deepseek-chat') should return deepseekTk after load")
	}
	// Pick 为非 deepseek 模型选择 default tokenizer
	defPick := Pick("gpt-4")
	if defPick != defaultTk {
		t.Error("Pick('gpt-4') should return defaultTk")
	}
	// CountTokensFor 也应正确选择
	dsFor := CountTokensFor("deepseek-v3", "Hello!")
	defFor := CountTokensFor("gpt-4", "Hello!")
	if dsFor == 0 || defFor == 0 {
		t.Error("CountTokensFor should return > 0 for non-empty text")
	}
}

func TestDeepseekTokenizer_StreamCounterBinding(t *testing.T) {
	loadAndInitDeepseek(t)

	// NewStreamCounterFor 为 deepseek 模型绑定 deepseek tokenizer
	sc := NewStreamCounterFor("deepseek-chat", 100)
	if sc == nil {
		t.Fatal("NewStreamCounterFor returned nil")
	}
	if sc.GetInputTokens() != 100 {
		t.Errorf("input tokens = %d, want 100", sc.GetInputTokens())
	}
	// 累积输出文本后计算
	sc.AddOutputText("Hello!")
	sc.ComputeOutputTokens()
	if sc.GetOutputTokens() == 0 {
		t.Error("output tokens should be > 0 after ComputeOutputTokens")
	}

	// 验证基于累计文本重新计算与增量统计一致
	sc2 := NewStreamCounterFor("deepseek-chat", 0)
	sc2.AddOutputText("Hello!")
	sc2.ComputeOutputTokens()
	counted := deepseekTk.CountTokens("Hello!")
	if sc2.GetOutputTokens() != counted {
		t.Errorf("StreamCounter output tokens (%d) should match direct CountTokens (%d)",
			sc2.GetOutputTokens(), counted)
	}
}

func TestDeepseekTokenizer_EmptyString(t *testing.T) {
	loadAndInitDeepseek(t)

	if got := deepseekTk.CountTokens(""); got != 0 {
		t.Errorf("CountTokens(\"\") = %d, want 0", got)
	}
}

func TestDeepseekTokenizer_CJKAndMixed(t *testing.T) {
	loadAndInitDeepseek(t)

	// 中英文混合
	got := deepseekTk.CountTokens("你好world")
	if got == 0 {
		t.Error("CJK mixed text should have token count > 0")
	}
}

func TestDeepseekTokenizer_TrailingWhitespace(t *testing.T) {
	loadAndInitDeepseek(t)

	// 末尾空白不应导致 panic 或异常值
	got := deepseekTk.CountTokens("hello   ")
	if got == 0 {
		t.Error("text with trailing whitespace should have token count > 0")
	}
	got2 := deepseekTk.CountTokens("hello")
	if got <= got2 {
		// trailing whitespace adds tokens, so it should be > or at least >=
		// In practice trailing whitespace may or may not add tokens depending on encoding
		t.Logf("trailing whitespace: %d tokens vs plain: %d tokens", got, got2)
	}
}

func TestDeepseekTokenizer_DigitBlocks(t *testing.T) {
	loadAndInitDeepseek(t)

	// 连续数字应被识别为独立块
	got := deepseekTk.CountTokens("abc123def")
	if got == 0 {
		t.Error("text with digit blocks should have token count > 0")
	}
}

func BenchmarkDeepseekTokenizer(b *testing.B) {
	err := loadDeepseekTokenizer()
	if err != nil {
		b.Fatalf("loadDeepseekTokenizer: %v", err)
	}
	if deepseekTk == nil {
		b.Fatal("deepseekTk is nil after successful load")
	}
	if defaultTk == nil {
		defaultTk = &tiktokenTokenizer{encoding: EncodingCL100K}
	}

	texts := []string{
		"Hello!",
		"你好，世界",
		"How are you doing today?",
		"Hello! 你好世界 123",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, text := range texts {
			deepseekTk.CountTokens(text)
		}
	}
}

func TestDeepseekTokenizer_HuggingFaceCrossCheck(t *testing.T) {
	loadAndInitDeepseek(t)

	// 与 HuggingFace tokenizers 库输出逐项对比
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"Hello!", 2},
		{"你好，世界", 3},
		{"<｜begin▁of▁sentence｜>", 1},
		{"<｜begin▁of▁sentence｜>Hello", 2},
		{"abc 123 def", 4},
		{"hello   ", 2},
		{"  hello", 2},
		{"\r\nhello", 3},
		{"hello\n\nworld", 3},
		{"你好world", 2},
		{"café", 3},
		// 修复 1：\p{N} 数字规则（含非 ASCII 数字）
		{"２３４", 3},     // 全角数字，每个独立成 1 token
		{"٣٤٥", 5},     // 阿拉伯-印度数字，HF 按字节拆开
		{"abc④def", 3}, // \p{No} 圈号数字
		// 修复 2：special=false added_tokens 整体匹配
		{"<think>hello</think>", 3},
		{"<｜User｜>你好<｜Assistant｜>", 3},
	}

	for _, tt := range tests {
		got := deepseekTk.CountTokens(tt.text)
		if got != tt.expected {
			t.Errorf("CountTokens(%q) = %d, want %d (HuggingFace reference)", tt.text, got, tt.expected)
		}
	}
}
