package httpapi

import (
	"testing"
)

func TestUsageFromAnthropicSSELine(t *testing.T) {
	start := usageFromAnthropicSSELine([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":8,"output_tokens":1}}}` + "\n"))
	if start.Input != 8 || start.Output != 1 || start.Total != 9 {
		t.Fatalf("message_start usage = %#v", start)
	}
	delta := usageFromAnthropicSSELine([]byte(`data: {"type":"message_delta","usage":{"output_tokens":5}}` + "\n"))
	got := mergeAnthropicUsage(start, delta)
	if got.Input != 8 || got.Output != 5 || got.Total != 13 {
		t.Fatalf("merged usage = %#v", got)
	}
	if got := usageFromAnthropicSSELine([]byte("event: ping\n")); got.Total != 0 || got.Input != 0 || got.Output != 0 {
		t.Fatalf("non-data line should not produce usage, got %#v", got)
	}
}
