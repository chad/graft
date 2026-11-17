package drafter

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Clause is one statement of intent, located in its source.
type Clause struct {
	Section  []string // heading path, outermost first
	Text     string
	Line     int // 1-based
	FromList bool
	Hash     string // content hash of the normalized text
}

var (
	reHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	reListItem = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	reSentence = regexp.MustCompile(`([.!?])\s+([A-Z` + "`" + `"“])`)
	reSpaces   = regexp.MustCompile(`\s+`)
)

// Normalize is the text a clause hash covers: case, whitespace, typographic
// quotes and a trailing period do not change meaning, so they do not change the hash.
func Normalize(s string) string {
	s = strings.NewReplacer("‘", "'", "’", "'", "“", `"`, "”", `"`).Replace(s)
	s = reSpaces.ReplaceAllString(strings.TrimSpace(s), " ")
	s = strings.TrimRight(s, ".")
	return strings.ToLower(s)
}

// ClauseHash is the content hash of a statement.
func ClauseHash(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:])
}

// Segment splits a markdown spec into clauses: each list item is one clause;
// paragraphs are split into sentences. Code fences are skipped.
func Segment(markdown string) []Clause {
	var (
		out     []Clause
		path    []string
		para    []string
		paraAt  int
		inFence bool
		inItem  bool // the previous line was a list item or its continuation
	)
	flush := func() {
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, " ")
		for _, s := range splitSentences(text) {
			out = append(out, newClause(path, s, paraAt, false))
		}
		para = nil
	}
	for i, line := range strings.Split(markdown, "\n") {
		n := i + 1
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flush()
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := reHeading.FindStringSubmatch(line); m != nil {
			flush()
			level := len(m[1])
			if level-1 < len(path) {
				path = path[:level-1]
			}
			for len(path) < level-1 {
				path = append(path, "")
			}
			path = append(path, m[2])
			continue
		}
		if m := reListItem.FindStringSubmatch(line); m != nil {
			flush()
			inItem = false
			if t := strings.TrimSpace(m[1]); t != "" {
				out = append(out, newClause(path, t, n, true))
				inItem = true
			}
			continue
		}
		if trimmed == "" {
			flush()
			inItem = false
			continue
		}
		// An indented line straight after a list item continues that item.
		if inItem && trimmed != line {
			last := &out[len(out)-1]
			*last = newClause(last.Section, last.Text+" "+trimmed, last.Line, true)
			continue
		}
		inItem = false
		if len(para) == 0 {
			paraAt = n
		}
		para = append(para, trimmed)
	}
	flush()
	return out
}

func newClause(path []string, text string, line int, fromList bool) Clause {
	return Clause{Section: append([]string(nil), path...), Text: text, Line: line, FromList: fromList, Hash: ClauseHash(text)}
}

func splitSentences(text string) []string {
	marked := reSentence.ReplaceAllString(text, "$1\x00$2")
	var out []string
	for _, s := range strings.Split(marked, "\x00") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
