package session

import (
	"regexp"
	"strings"
)

// Redaction copied from discrawl's internal/store/failures.go sanitizeFailureMessage
// (family house style), with the field-name alternation extended to cover the
// Instagram web-session secrets that leak into net/http error strings and cookie
// headers exactly the way bearer tokens do. Every error/log string that might
// have touched a request or response MUST pass through Sanitize before it is
// printed or persisted.

const maxFailureMessage = 2048

var (
	failureURLPattern    = regexp.MustCompile(`(?i)\bhttps?://[^\s,;]+`)
	failureBearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`)
	// Header-shaped: "sessionid: <x>", "X-CSRFToken: <x>", "Cookie: <x>", "Authorization: <x>", etc.
	failureHeaderPattern = regexp.MustCompile(`(?i)(\b[a-z0-9_.-]*(?:token|key|secret|authorization|sessionid|csrftoken|ds_user_id|ig_did|www-claim|cookie|mid|rur)[a-z0-9_.-]*\s*:\s*)(?:(?:bearer|basic)\s+)?[^\s,;]+`)
	// Query/pair-shaped: "sessionid=<x>", "?csrftoken=<x>".
	failureSecretPattern = regexp.MustCompile(`(?i)([?&]?[a-z0-9_.-]*(?:token|key|secret|authorization|sessionid|csrftoken|ds_user_id|ig_did|www-claim|cookie|mid|rur)[a-z0-9_.-]*=)[^&\s,;]+`)
	// JSON-shaped: "sessionid":"<x>".
	failureJSONPattern = regexp.MustCompile(`(?i)("[a-z0-9_.-]*(?:token|key|secret|authorization|sessionid|csrftoken|ds_user_id|ig_did|www[_-]claim|cookie|mid|rur)[a-z0-9_.-]*"\s*:\s*")[^"]+`)
)

// Sanitize redacts secrets and URLs from an arbitrary message and bounds its
// length. Safe to call on any string before logging or persisting it.
func Sanitize(message string) string {
	message = strings.ToValidUTF8(message, "")
	message = strings.Join(strings.Fields(message), " ")
	message = failureURLPattern.ReplaceAllString(message, "[redacted-url]")
	message = failureHeaderPattern.ReplaceAllString(message, `${1}[redacted]`)
	message = failureBearerPattern.ReplaceAllString(message, `${1}[redacted]`)
	message = failureSecretPattern.ReplaceAllString(message, `${1}[redacted]`)
	message = failureJSONPattern.ReplaceAllString(message, `${1}[redacted]`)
	runes := []rune(message)
	if len(runes) > maxFailureMessage {
		message = string(runes[:maxFailureMessage])
	}
	return message
}
