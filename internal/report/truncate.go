package report

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// TruncatePlan shortens text to at most maxBytes bytes. When text is longer
// it is cut after the last complete line that fits and TruncationNote is
// appended on a line of its own; a first line too long to keep whole is cut
// at a UTF-8 boundary instead. ok reports whether anything was cut. When not
// even the note fits, the result is empty.
func TruncatePlan(text string, maxBytes int) (out string, ok bool) {
	if len(text) <= maxBytes {
		return text, false
	}
	note := TruncationNote + "\n"
	room := maxBytes - len(note)
	if room < 0 {
		return "", true
	}
	cut := strings.LastIndexByte(text[:room], '\n') + 1
	if cut == 0 && room > 1 {
		r := room - 1
		for r > 0 && !utf8.RuneStart(text[r]) {
			r--
		}
		if r > 0 {
			return text[:r] + "\n" + note, true
		}
	}
	return text[:cut] + note, true
}

func fenceFor(text string) string {
	return strings.Repeat("`", max(3, longestRun(text, '`')+1))
}

type block struct {
	open, body, close string
}

func fencedBlock(prefix, body, lang, suffix string) block {
	f := fenceFor(body)
	return block{open: prefix + f + lang + "\n", body: body, close: f + "\n" + suffix}
}

func (b block) overhead() int { return len(b.open) + len(b.close) + 1 }

func (b block) render(limit int) string {
	body, _ := TruncatePlan(b.body, limit)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return b.open + body + b.close
}

func (b block) fit(total int) string { return b.render(max(total-b.overhead(), 0)) }

func shareBudget(sizes []int, budget int) []int {
	alloc := make([]int, len(sizes))
	order := make([]int, len(sizes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return sizes[order[a]] < sizes[order[b]] })
	remaining := max(budget, 0)
	for i, idx := range order {
		share := remaining / (len(order) - i)
		alloc[idx] = min(sizes[idx], share)
		remaining -= alloc[idx]
	}
	return alloc
}
