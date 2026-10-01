package application

import (
	"strings"
	"unicode/utf8"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
)

// RedactFields is the redaction policy of every structured record the SDK
// produces. It is a pure function: it takes the policy values the caller read
// from its versioned contract asset and returns a new, bounded, JSON-safe slice,
// so this layer spells no key name, no placeholder and no bound of its own.
//
// The policy has two tiers. A key that contains a fragment of alwaysRedacted,
// compared case insensitively, is dropped unconditionally: nothing of that field
// reaches the output, not even its key. A key that contains a fragment of
// contractRedacted has its value replaced by placeholder. Everything else is
// kept, but the whole record is bounded: at most maxFields fields survive, a
// key longer than maxKey or a key or value that is empty after sanitisation drops
// its field, a value longer than maxValue is truncated at a rune boundary, a
// repeated key keeps only its first occurrence, and control characters are
// removed so a value can never break a newline-delimited JSON line.
//
// The function never returns the input slice and never retains a field that was
// redacted or dropped.
func RedactFields(contractRedactedKeys []string, alwaysRedacted []string, placeholder string,
	maxFields, maxKey, maxValue int, fields []interfaces.Field) []interfaces.Field {
	if maxFields <= 0 || maxKey <= 0 || maxValue <= 0 || len(fields) == 0 {
		return nil
	}
	redacted := keyFragments(contractRedactedKeys)
	forbidden := keyFragments(alwaysRedacted)

	bounded := make([]interfaces.Field, 0, min(len(fields), maxFields))
	seen := make(map[string]struct{}, min(len(fields), maxFields))
	for _, field := range fields {
		if len(bounded) == maxFields {
			break
		}
		key, ok := sanitizeKey(field.Key, maxKey)
		if !ok {
			continue
		}
		if _, repeated := seen[key]; repeated {
			continue
		}
		if matchesFragment(key, forbidden) {
			continue
		}
		seen[key] = struct{}{}
		if matchesFragment(key, redacted) {
			if placeholder == "" {
				continue
			}
			value, ok := sanitize(placeholder, maxValue)
			if !ok {
				continue
			}
			bounded = append(bounded, interfaces.Field{Key: key, Value: value})
			continue
		}
		value, ok := sanitize(field.Value, maxValue)
		if !ok {
			continue
		}
		bounded = append(bounded, interfaces.Field{Key: key, Value: value})
	}
	if len(bounded) == 0 {
		return nil
	}
	return bounded
}

// keyFragments normalises the policy key lists: fragments are compared lower
// case, and an empty fragment is dropped because it would match every key.
func keyFragments(fragments []string) []string {
	normalized := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		fragment = strings.ToLower(fragment)
		if fragment == "" {
			continue
		}
		normalized = append(normalized, fragment)
	}
	return normalized
}

func matchesFragment(key string, fragments []string) bool {
	lowered := strings.ToLower(key)
	for _, fragment := range fragments {
		if strings.Contains(lowered, fragment) {
			return true
		}
	}
	return false
}

// sanitizeKey returns a usable key or reports that the field must be dropped. A
// key longer than limit is never truncated: a cut key would name a field the
// record does not hold, and could collide with a different real key, so the
// field is dropped instead.
func sanitizeKey(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", false
	}
	mapped := strings.Map(stripUnsafe, value)
	if mapped == "" || len(mapped) > limit {
		return "", false
	}
	return mapped, true
}

// sanitize returns a bounded, valid UTF-8, control-character-free text that is
// safe to serialise into a structured record. It reports false when nothing
// usable is left within the limit.
func sanitize(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", false
	}
	mapped := strings.Map(stripUnsafe, value)
	mapped = truncate(mapped, limit)
	if mapped == "" {
		return "", false
	}
	return mapped, true
}

// stripUnsafe neutralises the bytes that would corrupt a newline-delimited JSON
// record. Invalid UTF-8 becomes the replacement character, and control characters
// are dropped or replaced by a single space, so a value can never inject a line
// break or a record separator into operator output.
func stripUnsafe(character rune) rune {
	switch {
	case character == '\n' || character == '\r' || character == '\t':
		return ' '
	case character < 0x20 || character == 0x7f:
		return -1
	default:
		return character
	}
}

// truncate cuts a sanitised string to at most limit bytes without splitting a
// rune, so the result stays valid UTF-8.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	trimmed := value[:limit]
	for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}
