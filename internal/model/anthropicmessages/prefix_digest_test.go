package anthropicmessages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type thinkingConversation struct {
	t        *testing.T
	client   Client
	system   string
	messages []modelcontext.Message
}

func newThinkingConversation(t *testing.T, system string) *thinkingConversation {
	t.Helper()
	return &thinkingConversation{
		t: t,
		client: Client{
			ModelProviderConfigID: testModelProviderConfigID,
			EndpointPath:          testEndpointPath,
			ProviderModelSlug:     "claude-test",
		},
		system: system,
	}
}

func (c *thinkingConversation) user(text string) {
	c.messages = append(c.messages, modelcontext.Message{
		Role:     modelprotocol.RoleUser,
		Sequence: int64(len(c.messages) + 1),
		Content:  json.RawMessage(`[{"type":"text","text":` + jsonString(text) + `}]`),
	})
}

func (c *thinkingConversation) prepare(checkpoint *modelcontext.CheckpointRef) model.PreparedRequest {
	c.t.Helper()
	prepared, err := c.client.Prepare(c.t.Context(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt:      c.system,
			Messages:          c.messages,
			ContextCheckpoint: checkpoint,
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	require.NoError(c.t, err)
	require.Len(c.t, prepared.ProviderReplayPrefixDigest, 64)
	return prepared
}

// respond prepares the next request and records a signed thinking output bound
// to that request's prefix, as the kernel stores a successful response.
func (c *thinkingConversation) respond(signature, answer string) model.PreparedRequest {
	c.t.Helper()
	prepared := c.prepare(nil)
	c.messages = append(c.messages, modelcontext.Message{
		Role:               modelprotocol.RoleAssistant,
		Sequence:           int64(len(c.messages) + 1),
		ModelCallContextID: "mcc_" + signature,
		Content:            json.RawMessage(`[{"type":"text","text":` + jsonString(answer) + `}]`),
		ProviderReplay: json.RawMessage(`[{"type":"thinking","thinking":"","signature":` + jsonString(signature) +
			`},{"type":"text","text":` + jsonString(answer) + `}]`),
		ProviderReplaySource: testProviderReplayIdentity(
			c.client.ProviderModelSlug,
			modelprotocol.APIFormatAnthropicMessages,
		),
		ProviderReplayPrefixDigest: prepared.ProviderReplayPrefixDigest,
	})
	return prepared
}

func jsonString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func requireSignatures(t *testing.T, body json.RawMessage, present []string, absent []string) {
	t.Helper()
	for _, signature := range present {
		require.Contains(t, string(body), `"signature":"`+signature+`"`, "thinking %s should replay", signature)
	}
	for _, signature := range absent {
		require.NotContains(t, string(body), `"signature":"`+signature+`"`, "thinking %s should be stripped", signature)
	}
}

func TestPrepareReplaysThinkingWhileItsPrefixIsUnchanged(t *testing.T) {
	conversation := newThinkingConversation(t, "system v1")
	conversation.user("first")
	conversation.respond("sig_1", "answer one")
	conversation.user("second")
	conversation.respond("sig_2", "answer two")
	conversation.user("third")

	prepared := conversation.prepare(nil)

	requireSignatures(t, prepared.Body, []string{"sig_1", "sig_2"}, nil)
}

func TestPrepareStripsThinkingBoundToAChangedSystemPromptAndKeepsLaterThinking(t *testing.T) {
	conversation := newThinkingConversation(t, "system v1")
	conversation.user("first")
	conversation.respond("sig_1", "answer one")
	conversation.user("second")
	conversation.system = "system v2"
	conversation.respond("sig_2", "answer two")
	conversation.user("third")

	prepared := conversation.prepare(nil)

	requireSignatures(t, prepared.Body, []string{"sig_2"}, []string{"sig_1"})
	require.Contains(t, string(prepared.Body), "answer one", "stripped turns keep their text")
}

func TestPrepareStripsThinkingFromTurnsKeptAfterCompaction(t *testing.T) {
	conversation := newThinkingConversation(t, "system")
	conversation.user("first")
	conversation.respond("sig_1", "answer one")
	conversation.user("second")
	conversation.respond("sig_2", "answer two")
	conversation.messages = conversation.messages[2:]
	conversation.user("third")
	checkpoint := &modelcontext.CheckpointRef{SummarizedThroughEventSequence: 2, Summary: "summary"}

	prepared := conversation.prepare(checkpoint)

	requireSignatures(t, prepared.Body, nil, []string{"sig_1", "sig_2"})
	require.Contains(t, string(prepared.Body), "answer two")
}

func TestPrepareDigestMatchesTheNextRequestWhenHistoryOpensWithAnAssistantTurn(t *testing.T) {
	conversation := newThinkingConversation(t, "system")
	conversation.respond("sig_1", "opening answer")
	conversation.user("next")

	prepared := conversation.prepare(nil)

	requireSignatures(t, prepared.Body, []string{"sig_1"}, nil)
	require.Contains(t, string(prepared.Body), `"text":"Continue."`)
}

func TestPrepareKeepsThinkingAcrossClaudeModelSwitch(t *testing.T) {
	conversation := newThinkingConversation(t, "system")
	conversation.user("first")
	conversation.client.ProviderModelSlug = "claude-opus-5"
	conversation.respond("sig_1", "answer one")
	conversation.user("second")
	conversation.client.ProviderModelSlug = "claude-opus-5-5"

	prepared := conversation.prepare(nil)

	requireSignatures(t, prepared.Body, []string{"sig_1"}, nil)
}

func TestPrepareStripsReplayFromNonClaudeModelSwitch(t *testing.T) {
	conversation := newThinkingConversation(t, "system")
	conversation.user("first")
	conversation.client.ProviderModelSlug = "minimax-m3"
	conversation.respond("sig_1", "answer one")
	conversation.user("second")
	conversation.client.ProviderModelSlug = "claude-opus-5-5"

	prepared := conversation.prepare(nil)

	requireSignatures(t, prepared.Body, nil, []string{"sig_1"})
	require.Contains(t, string(prepared.Body), "answer one")
}

func TestRequestPrefixDigestIgnoresWhatThinkingIsNotBoundTo(t *testing.T) {
	base := `{"model":"claude-test","max_tokens":64,"system":"system",
		"tools":[
			{"name":"b","input_schema":{"type":"object"}},
			{"name":"a","description":"tool a","input_schema":{"properties":{},"type":"object"}}
		],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	variant := `{"max_tokens":32,"model":"claude-other","stream":true,
		"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],
		"tools":[
			{"name":"a","description":"tool a","input_schema":{"type":"object","properties":{}},
			 "cache_control":{"type":"ephemeral"}},
			{"name":"b","input_schema":{"type":"object"}},
			{"name":"late","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages":[{"role":"user","content":"hi"}]}`
	baseDigest, err := requestPrefixDigest(json.RawMessage(base))
	require.NoError(t, err)
	variantDigest, err := requestPrefixDigest(json.RawMessage(variant))
	require.NoError(t, err)
	require.Equal(t, baseDigest, variantDigest)

	for name, changed := range map[string]string{
		"system":           strings.Replace(base, `"system":"system"`, `"system":"system!"`, 1),
		"tool description": strings.Replace(base, `"tool a"`, `"tool A"`, 1),
		"message":          strings.Replace(base, `"text":"hi"`, `"text":"hi!"`, 1),
	} {
		digest, err := requestPrefixDigest(json.RawMessage(changed))
		require.NoError(t, err)
		require.NotEqual(t, baseDigest, digest, name)
	}
}
