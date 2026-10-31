package commands

import (
	"unicode"
	"unicode/utf8"
)

// MaskPhone hides all but the leading "+" and the last three digits.
func MaskPhone(phone string) string {
	prefix := ""
	if len(phone) > 0 && phone[0] == '+' {
		prefix, phone = "+", phone[1:]
	}
	r := []rune(phone)
	if len(r) <= 3 {
		return prefix + maskRunes(len(r))
	}
	return prefix + maskRunes(len(r)-3) + string(r[len(r)-3:])
}

func maskRunes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '*'
	}
	return string(b)
}

// Preview returns the length of text in runes and its first 20 runes after
// dropping control (Cc) and format (Cf) characters, so the result cannot forge
// log lines or reorder text with bidi marks. It still contains user content:
// use it ONLY for the audit log, never for application logs.
func Preview(text string) (length int, first20 string) {
	length = utf8.RuneCountInString(text)
	r := make([]rune, 0, 20)
	for _, c := range text {
		if unicode.In(c, unicode.Cc, unicode.Cf) {
			continue
		}
		if r = append(r, c); len(r) == 20 {
			break
		}
	}
	return length, string(r)
}
