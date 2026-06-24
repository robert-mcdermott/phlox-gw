package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

type guardrailEvaluation struct {
	Action   string   `json:"action"`
	Findings []string `json:"findings"`
	Redacted bool     `json:"redacted"`
	Blocked  bool     `json:"blocked"`
}

type guardrailTextResult struct {
	Text     string
	Findings []string
	Redacted bool
	Blocked  bool
}

type guardrailPlugin interface {
	Name() string
	ApplyText(text string, policy store.GuardrailPolicy) (string, []string, bool)
}

type piiGuardrailPlugin struct{}

type piiRule struct {
	Name    string
	Enabled func(store.GuardrailPolicy) bool
	Pattern *regexp.Regexp
	Valid   func(string) bool
}

var builtinPIIGuardrail guardrailPlugin = piiGuardrailPlugin{}

const (
	maxGuardrailCustomPatterns  = 100
	maxGuardrailRegexLength     = 1000
	defaultCustomRedactionToken = "[REDACTED]"
)

var piiRules = []piiRule{
	{
		Name:    "email",
		Enabled: func(p store.GuardrailPolicy) bool { return p.DetectEmail },
		Pattern: regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`),
	},
	{
		Name:    "phone",
		Enabled: func(p store.GuardrailPolicy) bool { return p.DetectPhone },
		Pattern: regexp.MustCompile(`\b(?:\+?1[\s.\-]?)?(?:\([2-9][0-9]{2}\)|[2-9][0-9]{2})[\s.\-]?[0-9]{3}[\s.\-]?[0-9]{4}\b`),
	},
	{
		Name:    "ssn",
		Enabled: func(p store.GuardrailPolicy) bool { return p.DetectSSN },
		Pattern: regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`),
	},
	{
		Name:    "credit_card",
		Enabled: func(p store.GuardrailPolicy) bool { return p.DetectCreditCard },
		Pattern: regexp.MustCompile(`\b(?:[0-9][ -]*?){13,19}\b`),
		Valid: func(match string) bool {
			digits := digitsOnly(match)
			return len(digits) >= 13 && len(digits) <= 19 && luhnValid(digits)
		},
	},
	{
		Name:    "api_key",
		Enabled: func(p store.GuardrailPolicy) bool { return p.DetectAPIKey },
		Pattern: regexp.MustCompile(`(?i)\b(?:pgw-sk-[A-Za-z0-9_\-]{16,}|sk-[A-Za-z0-9_\-]{16,}|xox[baprs]-[A-Za-z0-9\-]{10,}|gh[pousr]_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_\-]{20,}|AKIA[0-9A-Z]{16})\b`),
	},
}

func (piiGuardrailPlugin) Name() string {
	return "builtin-pii"
}

func (piiGuardrailPlugin) ApplyText(text string, policy store.GuardrailPolicy) (string, []string, bool) {
	if text == "" {
		return text, nil, false
	}
	findings := map[string]bool{}
	redacted := text
	mutated := false
	for _, rule := range piiRules {
		if !rule.Enabled(policy) {
			continue
		}
		redacted = rule.Pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			if rule.Valid != nil && !rule.Valid(match) {
				return match
			}
			findings[rule.Name] = true
			mutated = true
			return policy.RedactionText
		})
	}
	return redacted, sortedFindingNames(findings), mutated
}

func applyGuardrailToText(text string, policy store.GuardrailPolicy, action string, forceRedactBlocked bool) guardrailTextResult {
	result := guardrailTextResult{Text: text}
	if text == "" || action == "off" {
		return result
	}

	findings := map[string]bool{}
	blocked := false
	customPatterns := compiledCustomGuardrailPatterns(policy)
	for _, rule := range piiRules {
		if !rule.Enabled(policy) {
			continue
		}
		if builtinRuleMatches(rule, text) {
			findings[rule.Name] = true
			if action == "block" {
				blocked = true
			}
		}
	}
	for _, rule := range customPatterns {
		if rule.Pattern.MatchString(text) {
			findings[rule.Finding] = true
			if action == "block" || rule.Action == "block" {
				blocked = true
			}
		}
	}

	result.Findings = sortedFindingNames(findings)
	if blocked && !forceRedactBlocked {
		result.Blocked = true
		return result
	}
	if action != "redact" && !forceRedactBlocked {
		return result
	}

	redacted := text
	for _, rule := range piiRules {
		if !rule.Enabled(policy) {
			continue
		}
		redacted = rule.Pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			if rule.Valid != nil && !rule.Valid(match) {
				return match
			}
			return policy.RedactionText
		})
	}
	for _, rule := range customPatterns {
		if rule.Action != "redact" && !forceRedactBlocked {
			continue
		}
		replacement := rule.RedactionText
		if replacement == "" {
			replacement = policy.RedactionText
		}
		redacted = rule.Pattern.ReplaceAllString(redacted, replacement)
	}
	result.Text = redacted
	result.Redacted = redacted != text
	return result
}

type compiledCustomGuardrailPattern struct {
	Name          string
	Action        string
	RedactionText string
	Finding       string
	Pattern       *regexp.Regexp
}

func compiledCustomGuardrailPatterns(policy store.GuardrailPolicy) []compiledCustomGuardrailPattern {
	if len(policy.CustomPatterns) == 0 {
		return nil
	}
	out := make([]compiledCustomGuardrailPattern, 0, len(policy.CustomPatterns))
	for _, pattern := range policy.CustomPatterns {
		if !pattern.Enabled || strings.TrimSpace(pattern.Pattern) == "" {
			continue
		}
		compiled, err := regexp.Compile(pattern.Pattern)
		if err != nil {
			continue
		}
		name := guardrailCustomPatternDisplay(pattern)
		action := pattern.Action
		if action != "block" {
			action = "redact"
		}
		redaction := strings.TrimSpace(pattern.RedactionText)
		if redaction == "" {
			redaction = defaultCustomRedactionToken
		}
		out = append(out, compiledCustomGuardrailPattern{
			Name:          name,
			Action:        action,
			RedactionText: redaction,
			Finding:       "custom:" + name,
			Pattern:       compiled,
		})
	}
	return out
}

func validateGuardrailCustomPatterns(patterns []store.GuardrailCustomPattern) error {
	if len(patterns) > maxGuardrailCustomPatterns {
		return fmt.Errorf("guardrail policy supports at most %d custom patterns", maxGuardrailCustomPatterns)
	}
	for _, pattern := range patterns {
		raw := strings.TrimSpace(pattern.Pattern)
		if raw == "" {
			continue
		}
		if len(raw) > maxGuardrailRegexLength {
			return fmt.Errorf("custom pattern %q is too long; limit is %d characters", guardrailCustomPatternDisplay(pattern), maxGuardrailRegexLength)
		}
		action := strings.TrimSpace(strings.ToLower(pattern.Action))
		if action != "" && action != "redact" && action != "block" {
			return fmt.Errorf("custom pattern %q action must be redact or block", guardrailCustomPatternDisplay(pattern))
		}
		if _, err := regexp.Compile(raw); err != nil {
			return fmt.Errorf("custom pattern %q has invalid regex: %w", guardrailCustomPatternDisplay(pattern), err)
		}
	}
	return nil
}

func guardrailCustomPatternDisplay(pattern store.GuardrailCustomPattern) string {
	if strings.TrimSpace(pattern.Name) != "" {
		return strings.TrimSpace(pattern.Name)
	}
	if strings.TrimSpace(pattern.ID) != "" {
		return strings.TrimSpace(pattern.ID)
	}
	return "custom"
}

func builtinRuleMatches(rule piiRule, text string) bool {
	for _, match := range rule.Pattern.FindAllString(text, -1) {
		if rule.Valid == nil || rule.Valid(match) {
			return true
		}
	}
	return false
}

func guardrailAction(policy store.GuardrailPolicy, phase string) string {
	if !policy.Enabled {
		return "off"
	}
	switch phase {
	case "input":
		return policy.InputAction
	case "output":
		return policy.OutputAction
	default:
		return "off"
	}
}

func applyGuardrailToMap(policy store.GuardrailPolicy, phase string, raw map[string]any) (map[string]any, guardrailEvaluation) {
	action := guardrailAction(policy, phase)
	eval := guardrailEvaluation{Action: action}
	if action == "off" {
		return raw, eval
	}
	cloned, _ := cloneGuardrailJSON(raw).(map[string]any)
	if cloned == nil {
		cloned = map[string]any{}
	}
	redacted, findings, mutated, blocked := redactJSONValue(cloned, policy, "", action, false)
	eval.Findings = findings
	if blocked {
		eval.Blocked = true
		return raw, eval
	}
	if action == "redact" && mutated {
		eval.Redacted = true
		if m, ok := redacted.(map[string]any); ok {
			return m, eval
		}
	}
	return raw, eval
}

func applyGuardrailToBody(policy store.GuardrailPolicy, phase string, body []byte) ([]byte, guardrailEvaluation, error) {
	action := guardrailAction(policy, phase)
	eval := guardrailEvaluation{Action: action}
	if action == "off" || len(body) == 0 {
		return body, eval, nil
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, eval, nil
	}
	redacted, findings, mutated, blocked := redactJSONValue(raw, policy, "", action, false)
	eval.Findings = findings
	if blocked {
		eval.Blocked = true
		return body, eval, nil
	}
	if action == "redact" && mutated {
		next, err := json.Marshal(redacted)
		if err != nil {
			return body, eval, err
		}
		eval.Redacted = true
		return next, eval, nil
	}
	return body, eval, nil
}

func applyOutputGuardrailsToResult(policy store.GuardrailPolicy, result upstreamResult) upstreamResult {
	if result.Body == nil || result.Status >= 400 {
		return result
	}
	body, eval, err := applyGuardrailToBody(policy, "output", result.Body)
	if err != nil || eval.Action == "off" {
		return result
	}
	if eval.Blocked {
		message := guardrailReason("output", eval.Findings)
		result.Status = http.StatusUnprocessableEntity
		result.Headers = http.Header{"Content-Type": []string{"application/json"}}
		result.Body = guardrailErrorBody(result.Protocol, message)
		result.ErrorText = message
		return result
	}
	if eval.Redacted {
		result.Body = body
		if result.Headers == nil {
			result.Headers = http.Header{}
		}
		if result.Headers.Get("Content-Type") == "" {
			result.Headers.Set("Content-Type", "application/json")
		}
	}
	return result
}

func applyGuardrailToSSELine(policy store.GuardrailPolicy, line []byte) []byte {
	if guardrailAction(policy, "output") != "redact" || len(line) == 0 {
		return line
	}
	text := string(line)
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "data:") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return line
	}
	var raw any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return line
	}
	redacted, _, mutated, _ := redactJSONValue(raw, policy, "", "redact", true)
	if !mutated {
		return line
	}
	body, err := json.Marshal(redacted)
	if err != nil {
		return line
	}
	return []byte("data: " + string(body) + lineEnding(text))
}

func applyGuardrailToStreamPayload(policy store.GuardrailPolicy, payload map[string]any) map[string]any {
	if guardrailAction(policy, "output") != "redact" {
		return payload
	}
	redacted, _, mutated, _ := redactJSONValue(cloneGuardrailJSON(payload), policy, "", "redact", true)
	if !mutated {
		return payload
	}
	if m, ok := redacted.(map[string]any); ok {
		return m
	}
	return payload
}

func guardrailRejectsStreamingOutput(policy store.GuardrailPolicy) bool {
	return policy.Enabled && policy.OutputAction == "block" && policy.StreamingBlockMode == "reject"
}

func redactJSONValue(value any, policy store.GuardrailPolicy, key string, action string, forceRedactBlocked bool) (any, []string, bool, bool) {
	findings := map[string]bool{}
	redacted, mutated, blocked := redactJSONValueInto(value, policy, key, action, forceRedactBlocked, findings)
	return redacted, sortedFindingNames(findings), mutated, blocked
}

func redactJSONValueInto(value any, policy store.GuardrailPolicy, key string, action string, forceRedactBlocked bool, findings map[string]bool) (any, bool, bool) {
	switch v := value.(type) {
	case map[string]any:
		mutated := false
		blocked := false
		for k, child := range v {
			next, childMutated, childBlocked := redactJSONValueInto(child, policy, k, action, forceRedactBlocked, findings)
			if childMutated {
				v[k] = next
				mutated = true
			}
			if childBlocked {
				blocked = true
			}
		}
		return v, mutated, blocked
	case []any:
		mutated := false
		blocked := false
		for i, child := range v {
			next, childMutated, childBlocked := redactJSONValueInto(child, policy, key, action, forceRedactBlocked, findings)
			if childMutated {
				v[i] = next
				mutated = true
			}
			if childBlocked {
				blocked = true
			}
		}
		return v, mutated, blocked
	case string:
		if guardrailSkipStringKey(key) {
			return v, false, false
		}
		result := applyGuardrailToText(v, policy, action, forceRedactBlocked)
		for _, name := range result.Findings {
			findings[name] = true
		}
		return result.Text, result.Redacted, result.Blocked
	default:
		return value, false, false
	}
}

func guardrailSkipStringKey(key string) bool {
	switch strings.ToLower(key) {
	case "id", "object", "model", "role", "type", "finish_reason", "stop_reason", "stop_sequence", "provider", "protocol":
		return true
	default:
		return false
	}
}

func cloneGuardrailJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = cloneGuardrailJSON(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = cloneGuardrailJSON(child)
		}
		return out
	default:
		return value
	}
}

func sortedFindingNames(findings map[string]bool) []string {
	if len(findings) == 0 {
		return nil
	}
	out := make([]string, 0, len(findings))
	for name := range findings {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func guardrailReason(phase string, findings []string) string {
	if len(findings) == 0 {
		return "guardrail policy blocked request"
	}
	return "guardrail policy blocked " + phase + " content: " + strings.Join(findings, ", ")
}

func guardrailErrorBody(protocol, message string) []byte {
	if protocol == "anthropic" {
		body, _ := json.Marshal(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "content_policy_violation",
				"message": message,
			},
		})
		return body
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "content_policy_violation",
			"code":    "guardrail_blocked",
		},
	})
	return body
}

func lineEnding(text string) string {
	if strings.HasSuffix(text, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(text, "\n") {
		return "\n"
	}
	return ""
}

func digitsOnly(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func luhnValid(digits string) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum > 0 && sum%10 == 0
}
