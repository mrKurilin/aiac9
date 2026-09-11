package main

import "unicode"

// Count terminal cells, not UTF-8 bytes. Combining marks use no extra cell;
// wide CJK and emoji use two. Emoji sequences may be conservatively overcounted.
func runeCells(r rune) int {
	if unicode.IsControl(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || (r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || (r >= 0xac00 && r <= 0xd7a3) || (r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe10 && r <= 0xfe19) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f000 && r <= 0x1faff) || (r >= 0x20000 && r <= 0x3fffd)) {
		return 2
	}
	return 1
}
func inputViewport(text []rune, cells int) string {
	if cells <= 0 {
		return ""
	}
	total := 0
	for _, r := range text {
		total += runeCells(r)
	}
	if total <= cells {
		return string(text)
	}
	available := cells - 1 // Ellipsis when the beginning scrolls off-screen.
	start := len(text)
	for start > 0 {
		width := runeCells(text[start-1])
		if width > available {
			break
		}
		available -= width
		start--
	}
	return "…" + string(text[start:])
}
