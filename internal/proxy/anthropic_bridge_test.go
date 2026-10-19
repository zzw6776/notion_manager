package proxy

import (
	"strings"
	"testing"
)

func TestExtractAnthropicSessionSalt(t *testing.T) {
	metadata := map[string]interface{}{
		"user_id": `{"device_id":"dev-1","session_id":"sess-123","account_uuid":""}`,
	}

	if got := extractAnthropicSessionSalt(metadata); got != "sess-123" {
		t.Fatalf("extractAnthropicSessionSalt() = %q, want %q", got, "sess-123")
	}
}

func TestExtractAnthropicSessionSaltFromPlainStrings(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]interface{}
		want     string
	}{
		{name: "prompt cache key", metadata: map[string]interface{}{"prompt_cache_key": " task-123 "}, want: "task-123"},
		{name: "session id", metadata: map[string]interface{}{"session_id": "session-123"}, want: "session-123"},
		{name: "conversation id", metadata: map[string]interface{}{"conversation_id": "conversation-123"}, want: "conversation-123"},
		{name: "user id", metadata: map[string]interface{}{"user_id": "user-123"}, want: "user-123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractAnthropicSessionSalt(tt.metadata); got != tt.want {
				t.Fatalf("extractAnthropicSessionSalt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputeSessionFingerprintForRequestSeparatesPromptCacheKeysAndModels(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a coding assistant."},
		{Role: "user", Content: "Fix the failing test."},
	}

	base := computeSessionFingerprintForRequest(messages, "task-a", "fireworks-kimi-k3")
	otherTask := computeSessionFingerprintForRequest(messages, "task-b", "fireworks-kimi-k3")
	otherModel := computeSessionFingerprintForRequest(messages, "task-a", "anthropic-opus-4-6")
	continued := computeSessionFingerprintForRequest(append(cloneChatMessages(messages),
		ChatMessage{Role: "assistant", Content: "I will inspect it."},
		ChatMessage{Role: "user", Content: "Continue."},
	), "task-a", "fireworks-kimi-k3")
	if base == otherTask {
		t.Fatal("different prompt_cache_key values must not reuse a Notion thread")
	}
	if base == otherModel {
		t.Fatal("different resolved models must not reuse a Notion thread")
	}
	if base != continued {
		t.Fatal("the same prompt_cache_key and model must keep the Notion thread across turns")
	}
}

func TestComputeSessionFingerprintForRequestUsesStableSaltIdentity(t *testing.T) {
	first := []ChatMessage{
		{Role: "system", Content: "System instructions v1"},
		{Role: "user", Content: "First user message"},
	}
	drifted := []ChatMessage{
		{Role: "system", Content: "Completely different system instructions"},
		{Role: "user", Content: "A different user message"},
		{Role: "assistant", Content: "Prior answer"},
		{Role: "tool", Name: "Read", ToolCallID: "call_1", Content: "tool history"},
	}

	fp1 := computeSessionFingerprintForRequest(first, "task-stable", "fireworks-kimi-k3")
	fp2 := computeSessionFingerprintForRequest(drifted, "task-stable", "fireworks-kimi-k3")
	if fp1 != fp2 {
		t.Fatalf("same prompt_cache_key and resolved model must remain stable across message drift: %s != %s", fp1, fp2)
	}

	fallback1 := computeSessionFingerprintForRequest(first, "", "fireworks-kimi-k3")
	fallback2 := computeSessionFingerprintForRequest(drifted, "", "fireworks-kimi-k3")
	if fallback1 == fallback2 {
		t.Fatal("empty session salt must fall back to the model and message fingerprint")
	}
}

func TestComputeSessionFingerprintWithSalt_IgnoresBillingHeaderDrift(t *testing.T) {
	turn1 := []ChatMessage{
		{Role: "system", Content: "x-anthropic-billing-header: cc_version=2.1.81.a; cch=aaaa;\nYou are Claude Code, Anthropic's official CLI for Claude.\nSystem body"},
		{Role: "user", Content: "<available-deferred-tools>\nGrep\nRead\n</available-deferred-tools>"},
	}
	turn2 := []ChatMessage{
		{Role: "system", Content: "x-anthropic-billing-header: cc_version=2.1.81.b; cch=bbbb;\nYou are Claude Code, Anthropic's official CLI for Claude.\nSystem body"},
		{Role: "user", Content: "<available-deferred-tools>\nGrep\nRead\n</available-deferred-tools>"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Grep", Arguments: `{"pattern":"copy"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Name: "Grep", Content: "Found 1 file\nsrc/content.js"},
	}

	fp1 := computeSessionFingerprintWithSalt(turn1, "sess-123")
	fp2 := computeSessionFingerprintWithSalt(turn2, "sess-123")
	if fp1 != fp2 {
		t.Fatalf("fingerprint drifted across billing-header changes: %s vs %s", fp1, fp2)
	}
}

func TestApplyStructuredOutputBridge_JSONSchema(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "x-anthropic-billing-header: cc_version=2.1.81; cch=aaaa;"},
		{Role: "system", Content: "You are Claude Code, Anthropic's official CLI for Claude."},
		{Role: "system", Content: "Generate a concise title.\nReturn JSON with a single \"title\" field."},
		{Role: "user", Content: "检查为什么右侧预览栏的md copy按钮出不来"},
	}
	cfg := &AnthropicOutputConfig{
		Format: &AnthropicOutputFormat{
			Type: "json_schema",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{"type": "string"},
				},
				"required":             []string{"title"},
				"additionalProperties": false,
			},
		},
	}

	bridged := applyStructuredOutputBridge(messages, cfg)
	if len(bridged) != 1 {
		t.Fatalf("expected 1 bridged message, got %d", len(bridged))
	}
	if bridged[0].Role != "user" {
		t.Fatalf("expected bridged role=user, got %s", bridged[0].Role)
	}

	content := bridged[0].Content
	if strings.Contains(content, "x-anthropic-billing-header") {
		t.Fatalf("structured output bridge leaked billing header: %s", content)
	}
	if strings.Contains(content, "You are Claude Code") {
		t.Fatalf("structured output bridge leaked Claude identity line: %s", content)
	}
	if !strings.Contains(content, `Return JSON with a single "title" field.`) {
		t.Fatalf("structured output bridge dropped system instruction: %s", content)
	}
	if !strings.Contains(content, "检查为什么右侧预览栏的md copy按钮出不来") {
		t.Fatalf("structured output bridge dropped user content: %s", content)
	}
	if !strings.Contains(content, `"title": {`) || !strings.Contains(content, `"required": [`) {
		t.Fatalf("structured output bridge did not embed schema JSON: %s", content)
	}
}

func TestInjectToolsIntoMessages_DropsWrapperOnlyUserMessage(t *testing.T) {
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "Bash", Description: "Execute shell command", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Read", Description: "Read a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Write", Description: "Write a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Edit", Description: "Edit a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Glob", Description: "Find files", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Grep", Description: "Search files", Parameters: map[string]interface{}{"type": "object"}}},
	}
	messages := []ChatMessage{
		{Role: "system", Content: "You are Claude Code."},
		{Role: "user", Content: "<available-deferred-tools>\nRead\nEdit\n</available-deferred-tools>"},
		{Role: "user", Content: "修复登录校验"},
	}

	got := injectToolsIntoMessages(messages, tools, "claude-opus-4-6", nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 bridged message, got %d", len(got))
	}

	content := got[0].Content
	if strings.Contains(content, "User: Hello") || strings.Contains(content, "\nHello\n") {
		t.Fatalf("wrapper-only message should not turn into synthetic Hello: %q", content)
	}
	if strings.Contains(content, "<available-deferred-tools>") {
		t.Fatalf("wrapper-only message leaked into bridged content: %q", content)
	}
	if !strings.Contains(content, `Input: "修复登录校验"`) {
		t.Fatalf("expected actual user query in bridged content, got %q", content)
	}
}

func TestNormalizeStructuredOutputText_StripsLangTagAndMarkdownFence(t *testing.T) {
	raw := "<lang primary=\"zh-CN\"/>\n\n```json\n{\"title\":\"Fix digest error\"}\n```"
	got := normalizeStructuredOutputText(raw)
	want := "{\"title\":\"Fix digest error\"}"
	if got != want {
		t.Fatalf("normalizeStructuredOutputText() = %q, want %q", got, want)
	}
}

func TestNormalizeStructuredOutputText_ExtractsJSONObjectFromPrefixedText(t *testing.T) {
	raw := "Here is the JSON output you requested:\n{\"title\":\"Fix invalid password\"}"
	got := normalizeStructuredOutputText(raw)
	want := "{\"title\":\"Fix invalid password\"}"
	if got != want {
		t.Fatalf("normalizeStructuredOutputText() = %q, want %q", got, want)
	}
}

func TestDetectToolBridgeNoToolResponse_MatchesIdentityDriftHandOff(t *testing.T) {
	raw := `<lang primary="zh-CN"/>

抱歉，我理解你希望我直接帮你修改文件，但**我是 Notion AI，无法访问你的本地文件系统**。我没有 Read、Edit、Bash 这些工具的能力。

把下面这段话直接发给你的编码助手（Cursor / Claude Code），它就能帮你操作。`

	if !detectToolBridgeNoToolResponse(raw) {
		t.Fatalf("expected no-tool identity drift text to be detected")
	}
}

func TestDetectToolBridgeNoToolResponse_DoesNotMatchNormalAnswer(t *testing.T) {
	raw := "我已经根据上面的 grep 结果定位到文件，下一步建议缩小 Read 范围后继续编辑。"

	if detectToolBridgeNoToolResponse(raw) {
		t.Fatalf("normal answer should not be classified as no-tool identity drift")
	}
}

func TestSanitizeLLMJSON_RepairsInvalidEscapesAndTrailingCommas(t *testing.T) {
	input := `{"name": "test", "arguments": {"path": "C:\work\dir\test.txt", "regex": "\s+\d+", "broken": "\施顺序",},}`
	sanitized := sanitizeLLMJSON(input)
	if !strings.Contains(sanitized, `\\work\\dir`) {
		t.Fatalf("expected escaped backslashes in path, got: %s", sanitized)
	}
	if !strings.Contains(sanitized, `\\s+\\d+`) {
		t.Fatalf("expected escaped regex backslashes, got: %s", sanitized)
	}
	if !strings.Contains(sanitized, `\\施顺序`) {
		t.Fatalf("expected escaped non-ascii escape, got: %s", sanitized)
	}
}

func TestParseToolCalls_RepairsInvalidEscapeInMarkdownFence(t *testing.T) {
	content := "```json\n{\"name\": \"__done__\", \"arguments\": {\"result\": \"## 综合最优方案\\n\\n总体思路：不重写同步协议\\n\\施顺序\\n1. 第一步\"}}\n```"
	calls, remaining, ok := parseToolCalls(content)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected tool call to be parsed from fenced markdown with invalid escape, got: ok=%v, calls=%v", ok, calls)
	}
	if calls[0].Function.Name != "__done__" {
		t.Fatalf("expected __done__ function call, got: %s", calls[0].Function.Name)
	}
	if remaining != "" {
		t.Fatalf("expected empty remaining text, got: %q", remaining)
	}
}

func TestPrepareToolBridgeResponse_InterceptsDoneWithInvalidEscape(t *testing.T) {
	content := "```json\n{\"name\": \"__done__\", \"arguments\": {\"result\": \"## 综合最优方案\\n\\n总体思路：不重写同步协议\\n\\施顺序\\n1. 第一步\"}}\n```"
	prepared := prepareToolBridgeResponse(content, nil)
	if prepared.DoneText == "" {
		t.Fatalf("expected DoneText to be intercepted and extracted")
	}
	if !strings.Contains(prepared.DoneText, "## 综合最优方案") || !strings.Contains(prepared.DoneText, "施顺序") {
		t.Fatalf("unexpected DoneText content: %q", prepared.DoneText)
	}
	if prepared.HasCalls {
		t.Fatalf("expected HasCalls to be false after __done__ interception, got true")
	}
}

func TestPrepareToolBridgeResponse_FallbackDoneRecoveryWhenUnparsed(t *testing.T) {
	// A severely malformed output where JSON parser cannot parse the tool call object
	content := "```json\n{\"name\": \"__done__\", \"arguments\": {\"result\": \"Plan content here...\"} trailing gibberish"
	prepared := prepareToolBridgeResponse(content, nil)
	if prepared.DoneText == "" {
		t.Fatalf("expected fallback extractor to recover DoneText from unparsed __done__")
	}
	if prepared.DoneText != "Plan content here..." {
		t.Fatalf("expected 'Plan content here...', got: %q", prepared.DoneText)
	}
}

func TestCleanAllLangTags_PreservesCodeGenericsAndMultiline(t *testing.T) {
	// 1. Simulating msg_f8893cec: incomplete <lang prefix followed by multiline text with Rust Mutex<()>
	rawWithIncompleteLang := "<lang primary=\"en-\n\n```rust\nlet mut guard = self.lock.lock();\nlet x = Mutex<()>::new();\n```\nAll done."
	cleaned := cleanAllLangTags(rawWithIncompleteLang)
	want := "\n\n```rust\nlet mut guard = self.lock.lock();\nlet x = Mutex<()>::new();\n```\nAll done."
	if cleaned != want {
		t.Fatalf("cleanAllLangTags corrupted multiline code with generics:\ngot:\n%s\nwant:\n%s", cleaned, want)
	}

	// 2. Normal self-closing tag
	rawNormal := "<lang primary=\"zh-CN\"/>\n\nSome text here"
	if got := cleanAllLangTags(rawNormal); got != "\n\nSome text here" {
		t.Fatalf("cleanAllLangTags failed on normal self-closing tag: got %q", got)
	}

	// 3. Normal tag with multiple attributes
	rawMulti := "<lang primary=\"en-US\" secondary=\"zh-CN\"/>\n\nSome text here"
	if got := cleanAllLangTags(rawMulti); got != "\n\nSome text here" {
		t.Fatalf("cleanAllLangTags failed on multi-attr tag: got %q", got)
	}

	// 4. Code without any lang tag containing generics
	rawCode := "```rust\nlet v: Vec<String> = vec![];\nlet m: Arc<Mutex<Option<i32>>> = Arc::new(Mutex::new(None));\n```"
	if got := cleanAllLangTags(rawCode); got != rawCode {
		t.Fatalf("cleanAllLangTags damaged code without lang tag: got %q", got)
	}

	// 5. Code with expression containing '<lang' in condition
	rawExpr := "if a <lang && b > 10 {\n    println!(\"hello\");\n}"
	if got := cleanAllLangTags(rawExpr); got != rawExpr {
		t.Fatalf("cleanAllLangTags damaged expression: got %q", got)
	}
}

func TestHandlePatchReplace_SafePrefixReplacement(t *testing.T) {
	// 1. Incomplete <lang prefix at start should be replaced
	current := "<lang primary=\"en-US"
	replacement := "OK"
	if got := handlePatchReplace(current, replacement); got != "OK" {
		t.Fatalf("expected 'OK', got %q", got)
	}

	// 3. Incomplete tag split mid-attribute: '<lang prim' must become ''
	if got := handlePatchReplace("<lang prim", "{"); got != "{" {
		t.Fatalf("expected '{', got %q", got)
	}
}

func TestCleanAllLangTags_NeverLeaksPartialAttributeFragments(t *testing.T) {
	// Recreating the exact real-world scenario from 23:28
	// Notion sends `<lang prim` chunk, which must NEVER leak "prim"
	cases := []struct {
		input string
		want  string
	}{
		{"<lang prim", ""},
		{"<lang p", ""},
		{"<lang primary=\"z", ""},
		{"<lang primary=\"zh-CN\"", ""},
		{"<lang primary=\"zh-CN\"/", ""},
		{"<lang primary=\"zh-CN\"/>", ""},
		{"<lang prim{\"name\":\"exec_command\"}", "{\"name\":\"exec_command\"}"},
		{"<lang prim\n\n1. Mutex<()>", "\n\n1. Mutex<()>"},
		{"<lang primary=\"en-\n\n1. Mutex<()>", "\n\n1. Mutex<()>"},
	}

	for _, tc := range cases {
		got := cleanAllLangTags(tc.input)
		if got != tc.want {
			t.Fatalf("cleanAllLangTags(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseToolCalls_ResilientToNoisyPrefix(t *testing.T) {
	// Test direct JSON with a "prim" or garbage prefix
	content := `prim{"name":"exec_command","arguments":{"cmd":"echo hello"}}`
	calls, remaining, ok := parseToolCalls(content)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 tool call parsed despite 'prim' prefix, got ok=%v, calls=%v", ok, calls)
	}
	if calls[0].Function.Name != "exec_command" {
		t.Fatalf("expected 'exec_command', got %q", calls[0].Function.Name)
	}
	if remaining != "" {
		t.Fatalf("expected empty remaining, got %q", remaining)
	}
}

func TestParseToolCalls_SupportsFunctionAliasAndClaudeCodeBash(t *testing.T) {
	// Recreating the exact Claude Code response from screenshot:
	// {"function": "Bash", "arguments": {"command": "git status && git diff HEAD", "description": "查看未提交的代码变更以进行审查"}}
	content := `{"function": "Bash", "arguments": {"command": "git status && git diff HEAD", "description": "查看未提交的代码变更以进行审查"}}`
	calls, remaining, ok := parseToolCalls(content)
	if !ok || len(calls) != 1 {
		t.Fatalf("expected 1 tool call parsed for 'function' key, got ok=%v, calls=%v", ok, calls)
	}
	if calls[0].Function.Name != "Bash" {
		t.Fatalf("expected function name 'Bash', got %q", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, "git status && git diff HEAD") {
		t.Fatalf("expected arguments to contain command, got %q", calls[0].Function.Arguments)
	}
	if remaining != "" {
		t.Fatalf("expected empty remaining, got %q", remaining)
	}

	prepared := prepareToolBridgeResponse(content, nil)
	if !prepared.HasCalls {
		t.Fatalf("expected HasCalls to be true in prepareToolBridgeResponse")
	}
}


