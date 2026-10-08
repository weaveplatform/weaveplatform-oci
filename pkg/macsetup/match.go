package macsetup

import (
	"strings"
	"unicode"
)

// locate finds the controls labelled label: exact OCR matches when there are
// any, otherwise tolerant ones. Vision misreads or clips a button's label now
// and then — macOS 27's Analytics page read its Continue button only as "nue" —
// and an exact-only match then waited on the page until it timed out. Pages are
// still recognised by exact text (has); only the control to click is found this
// way, so a misread cannot change which page the plan thinks it is on.
func (s Screen) locate(label string) []Text {
	if m := s.exact(label); len(m) > 0 {
		return m
	}
	var out []Text
	for _, t := range s.Text {
		if box, ok := tolerantMatch(t, label); ok {
			out = append(out, box)
		}
	}
	return out
}

// tolerantMatch reports whether OCR text t is label misread or clipped, and
// returns it with its box widened to the whole label when it is a clipped
// fragment, so a click lands on the middle of the button.
//
// Only labels of six or more letters are matched tolerantly; shorter ones
// ("Skip", "Agree") are too easily another word. A misread may differ by one
// letter in six. A fragment must be at least a third of the label, at its start
// or its end, and cut through a word: "nue" is the end of "Continue", but "Use"
// is a whole word of "Don't Use" and so is more likely another control.
func tolerantMatch(t Text, label string) (Text, bool) {
	v, l := []rune(normalizeLabel(t.Value)), []rune(normalizeLabel(label))
	if len(l) < 6 || len(v) == 0 {
		return Text{}, false
	}
	if tolerance := len(l) / 6; abs(len(v)-len(l)) <= tolerance && editDistance(v, l) <= tolerance {
		return t, true
	}
	if len(v) < 3 || len(v)*3 < len(l) || len(v) >= len(l) {
		return Text{}, false
	}
	charWidth := float64(t.Width) / float64(len(v))
	whole := int(charWidth * float64(len(l)))
	switch {
	case string(l[len(l)-len(v):]) == string(v) && isLetter(l[len(l)-len(v)-1]) && isLetter(v[0]):
		missing := int(charWidth * float64(len(l)-len(v)))
		t.X = max(0, t.X-missing)
		t.Width = whole
		return t, true
	case string(l[:len(v)]) == string(v) && isLetter(l[len(v)]) && isLetter(v[len(v)-1]):
		t.Width = whole
		return t, true
	default:
		return Text{}, false
	}
}

// normalizeLabel folds the differences OCR introduces that never distinguish
// two controls: case, curly apostrophes, repeated spaces, and trailing
// disclosure glyphs.
func normalizeLabel(v string) string {
	v = strings.ReplaceAll(v, "’", "'")
	v = strings.Join(strings.Fields(strings.ToLower(v)), " ")
	return strings.TrimRight(v, " >›→⌄.…")
}

func isLetter(r rune) bool { return unicode.IsLetter(r) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
