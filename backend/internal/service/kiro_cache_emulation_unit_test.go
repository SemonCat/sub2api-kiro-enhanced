//go:build unit

package service

import (
	"fmt"
	"strings"
	"time"
)

func resetKiroCacheTracker() { globalKiroDynamicTracker = newKiroDynamicTracker(time.Now) }

func kiroChatCompletionsConversationBody(messages []string) []byte {
	items := make([]string, 0, len(messages)+1)
	items = append(items, `{"role":"system","content":"You are a precise assistant."}`)
	for _, message := range messages {
		items = append(items, fmt.Sprintf(`{"role":"user","content":%q}`, message))
	}
	return []byte(fmt.Sprintf(`{"model":"gpt-5","tool_choice":"auto","tools":[{"type":"function","function":{"name":"lookup","description":"lookup data","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}],"messages":[%s]}`, strings.Join(items, ",")))
}

func kiroResponsesCacheRequestBody(label, promptCacheKey, previousResponseID string) []byte {
	return kiroResponsesCacheRequestBodyWithOptions(label, promptCacheKey, previousResponseID, "gpt-5", "auto", "medium", `{"type":"json_object"}`, "lookup")
}

func kiroResponsesCacheRequestBodyWithOptions(label, promptCacheKey, previousResponseID, model, toolChoice, effort, textFormat, toolName string) []byte {
	prompt := strings.Repeat("cacheable responses prompt chunk "+label+" ", 512)
	return []byte(fmt.Sprintf(`{"model":%q,"instructions":"You are a precise assistant.","prompt_cache_key":%q,"previous_response_id":%q,"tool_choice":%q,"reasoning":{"effort":%q},"text":{"format":%s},"tools":[{"type":"function","name":%q,"description":"lookup data","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}],"input":[{"role":"user","content":[{"type":"input_text","text":%q}]}]}`, model, promptCacheKey, previousResponseID, toolChoice, effort, textFormat, toolName, prompt))
}
