package anthropicmessages

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
)

// Claude binds each thinking block to the conversation prefix that produced it:
// the system prompt, the non-deferred tools as a name-keyed set, and every
// message before the block. A prefix digest records that prefix as sent so a
// later request can tell, before sending, which replayed thinking blocks the
// API would reject. Cache markers are not part of the binding, so they are
// excluded.
type prefixHasher struct {
	system      string
	tools       []toolDefinition
	hash        hash.Hash
	role        anthropicRole
	messageOpen bool
}

func newPrefixHasher(system string, tools []toolDefinition) (*prefixHasher, error) {
	hasher := &prefixHasher{system: system, tools: tools, hash: sha256.New()}
	hasher.writeFrame("system", []byte(system))
	bound := make([]toolDefinition, 0, len(tools))
	for _, tool := range tools {
		if !tool.DeferLoading {
			bound = append(bound, tool)
		}
	}
	sort.Slice(bound, func(left, right int) bool { return bound[left].Name < bound[right].Name })
	for _, tool := range bound {
		schema, err := canonicalPrefixJSON(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("canonicalize tool %q schema: %w", tool.Name, err)
		}
		hasher.writeFrame("tool", []byte(tool.Name))
		hasher.writeFrame("description", []byte(tool.Description))
		hasher.writeFrame("schema", schema)
	}
	return hasher, nil
}

func (h *prefixHasher) appendBlocks(role anthropicRole, blocks []any) error {
	if len(blocks) == 0 {
		return nil
	}
	if !h.messageOpen || h.role != role {
		h.writeFrame("role", []byte(role))
		h.role = role
		h.messageOpen = true
	}
	for _, block := range blocks {
		raw, err := json.Marshal(block)
		if err != nil {
			return err
		}
		canonical, err := canonicalPrefixBlock(raw)
		if err != nil {
			return err
		}
		h.writeFrame("block", canonical)
	}
	return nil
}

// withOpeningBlocks returns a separate hasher for an empty prefix followed by
// one opening message; h is left unchanged.
func (h *prefixHasher) withOpeningBlocks(role anthropicRole, blocks []any) (*prefixHasher, error) {
	if !h.empty() {
		return nil, errors.New("prefix already has messages")
	}
	opened, err := newPrefixHasher(h.system, h.tools)
	if err != nil {
		return nil, err
	}
	if err := opened.appendBlocks(role, blocks); err != nil {
		return nil, err
	}
	return opened, nil
}

func (h *prefixHasher) empty() bool {
	return !h.messageOpen
}

func (h *prefixHasher) digest() string {
	return hex.EncodeToString(h.hash.Sum(nil))
}

func (h *prefixHasher) writeFrame(kind string, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(kind)))
	h.hash.Write(length[:])
	h.hash.Write([]byte(kind))
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	h.hash.Write(length[:])
	h.hash.Write(value)
}

func canonicalPrefixBlock(raw json.RawMessage) ([]byte, error) {
	value, err := decodePrefixJSON(raw)
	if err != nil {
		return nil, err
	}
	if block, ok := value.(map[string]any); ok {
		delete(block, "cache_control")
	}
	return json.Marshal(value)
}

func canonicalPrefixJSON(raw json.RawMessage) ([]byte, error) {
	value, err := decodePrefixJSON(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func decodePrefixJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// requestPrefixDigest digests the full prefix of a prepared request body: the
// prefix every thinking block in the response is bound to.
func requestPrefixDigest(body json.RawMessage) (string, error) {
	var request struct {
		System   json.RawMessage  `json:"system"`
		Tools    []toolDefinition `json:"tools"`
		Messages []struct {
			Role    anthropicRole   `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "", fmt.Errorf("decode anthropic-messages request: %w", err)
	}
	system, err := requestSystemText(request.System)
	if err != nil {
		return "", err
	}
	hasher, err := newPrefixHasher(system, request.Tools)
	if err != nil {
		return "", err
	}
	for index, message := range request.Messages {
		blocks, err := requestContentBlocks(message.Content)
		if err != nil {
			return "", fmt.Errorf("decode anthropic-messages message %d: %w", index, err)
		}
		if err := hasher.appendBlocks(message.Role, blocks); err != nil {
			return "", err
		}
	}
	return hasher.digest(), nil
}

func requestSystemText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []textBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("decode anthropic-messages system: %w", err)
	}
	var system bytes.Buffer
	for _, block := range blocks {
		system.WriteString(block.Text)
	}
	return system.String(), nil
}

func requestContentBlocks(raw json.RawMessage) ([]any, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []any{textBlock{Type: "text", Text: text}}, nil
	}
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(raw, &rawBlocks); err != nil {
		return nil, err
	}
	blocks := make([]any, 0, len(rawBlocks))
	for _, block := range rawBlocks {
		blocks = append(blocks, block)
	}
	return blocks, nil
}
