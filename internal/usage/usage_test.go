package usage

import (
	"testing"

	"github.com/jonas/opencode-go-router/internal/catalog"
)

func kimi(t *testing.T) catalog.Model {
	t.Helper()
	m, ok := catalog.Get("kimi-k3")
	if !ok {
		t.Fatal("missing kimi-k3 in catalog")
	}
	return m
}

func TestExtractOpenAIChatWithAllowance(t *testing.T) {
	raw := []byte(`{
		"id":"x","usage":{
			"prompt_tokens":120,"completion_tokens":30,
			"prompt_tokens_details":{"cached_tokens":20},
			"opencode":{"allowance":0.0042,"list_cost":0.0042,"billing_source":"go",
				"tokens":{"input":100,"output":30,"reasoning":5,"cache_read":20,"cache_write_5m":0,"cache_write_1h":0}}
		}}`)
	u := Extract(raw, "chat")
	u.Finalize("chat", kimi(t))
	if !u.Found {
		t.Fatal("usage not found")
	}
	if u.Input != 100 || u.Output != 30 || u.CacheRead != 20 || u.Reasoning != 5 {
		t.Fatalf("unexpected tokens: %+v", u)
	}
	if u.Cost != 0.0042 {
		t.Fatalf("cost = %v, want 0.0042", u.Cost)
	}
	if u.Tokens() != 150 {
		t.Fatalf("tokens = %d, want 150", u.Tokens())
	}
}

func TestExtractOpenAIWithoutOpenCodeTokens(t *testing.T) {
	raw := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":25,"prompt_tokens_details":{"cached_tokens":40}}}`)
	u := Extract(raw, "chat")
	u.Finalize("chat", catalog.Model{})
	if u.Input != 60 || u.Output != 25 || u.CacheRead != 40 {
		t.Fatalf("unexpected tokens: %+v", u)
	}
}

func TestExtractAnthropicStream(t *testing.T) {
	c := NewCollector("messages")
	c.Feed([]byte("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":50,\"cache_read_input_tokens\":1000,\"cache_creation_input_tokens\":10,\"output_tokens\":1}}}\n\n"))
	c.Feed([]byte("event: message_delta\ndata: {\"usage\":{\"output_tokens\":200},\"opencode\":{\"allowance\":0.01,\"tokens\":{\"input\":50,\"output\":200,\"cache_read\":1000,\"cache_write_5m\":10}}}\n\n"))
	c.Feed([]byte("event: message_stop\ndata: {}\n\n"))
	u := c.Usage()
	u.Finalize("messages", kimi(t))
	if u.Input != 50 || u.Output != 200 || u.CacheRead != 1000 || u.CacheWrite != 10 {
		t.Fatalf("unexpected tokens: %+v", u)
	}
	if u.Cost != 0.01 {
		t.Fatalf("cost = %v", u.Cost)
	}
}

func TestExtractResponsesStream(t *testing.T) {
	c := NewCollector("responses")
	c.Feed([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":7},"opencode":{"allowance":0.02,"tokens":{"input":60,"output":50,"cache_read":40}}}}}` + "\n\n"))
	u := c.Usage()
	u.Finalize("responses", kimi(t))
	if u.Input != 60 || u.Output != 50 || u.CacheRead != 40 || u.Reasoning != 7 {
		t.Fatalf("unexpected tokens: %+v", u)
	}
	if u.Cost != 0.02 {
		t.Fatalf("cost = %v", u.Cost)
	}
}

func TestExtractGemini(t *testing.T) {
	raw := []byte(`{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":40,"thoughtsTokenCount":3},"opencode":{"allowance":0.03}}`)
	u := Extract(raw, "google")
	u.Finalize("google", kimi(t))
	if u.Input != 60 || u.Output != 20 || u.CacheRead != 40 || u.Reasoning != 3 {
		t.Fatalf("unexpected tokens: %+v", u)
	}
	if u.Cost != 0.03 {
		t.Fatalf("cost = %v", u.Cost)
	}
}

func TestExtractErrorMessage(t *testing.T) {
	raw := []byte(`{"error":{"message":"model is not allowed","type":"invalid_request_error"}}`)
	if got := ErrorMessage(raw); got != "model is not allowed" {
		t.Fatalf("got %q", got)
	}
}
