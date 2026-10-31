package commands

import (
	"strings"
	"unicode/utf16"
)

const gsmBasic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ ÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

const gsmExtension = "^{}\\[~]|€"

// SMSSegments reports the encoding ("GSM-7" or "UCS-2"), the length in
// encoding units (septets, or UTF-16 code units) and the number of billed
// segments. Empty text has zero segments. Multipart messages are packed
// greedily: a GSM extension pair (2 septets) or a UTF-16 surrogate pair is
// never split across segments, so the count can exceed ceil(units/153|67).
func SMSSegments(text string) (enc string, chars, segments int) {
	var sizes []int
	gsm := true
	for _, r := range text {
		switch {
		case strings.ContainsRune(gsmBasic, r):
			sizes = append(sizes, 1)
		case strings.ContainsRune(gsmExtension, r):
			sizes = append(sizes, 2)
		default:
			gsm = false
		}
		if !gsm {
			break
		}
	}
	if gsm {
		n := sum(sizes)
		return "GSM-7", n, segmentCount(sizes, n, 160, 153)
	}
	sizes = sizes[:0]
	for _, r := range text {
		sizes = append(sizes, len(utf16.AppendRune(nil, r)))
	}
	n := sum(sizes)
	return "UCS-2", n, segmentCount(sizes, n, 70, 67)
}

func sum(xs []int) (n int) {
	for _, x := range xs {
		n += x
	}
	return n
}

// segmentCount packs unit sizes into parts of at most multi units when the
// message exceeds a single segment of single units.
func segmentCount(sizes []int, n, single, multi int) int {
	switch {
	case n == 0:
		return 0
	case n <= single:
		return 1
	}
	segs, cur := 1, 0
	for _, u := range sizes {
		if cur+u > multi {
			segs++
			cur = 0
		}
		cur += u
	}
	return segs
}
