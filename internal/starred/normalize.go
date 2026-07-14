package starred

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var whitespaceRE = regexp.MustCompile(`\s+`)

func NormalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func NormalizeClub(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "c&sc", " ")
	s = strings.ReplaceAll(s, "ccc", " ")
	s = strings.ReplaceAll(s, "cc", " ")
	s = strings.ReplaceAll(s, "cricket club", " ")
	s = strings.ReplaceAll(s, "lancs", " ")
	s = whitespaceRE.ReplaceAllString(s, " ")
	return NormalizeName(s)
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b && a < c {
		return a
	}
	if b < c {
		return b
	}
	return c
}

func closestUnique(target string, candidates []string) (string, bool) {
	type scored struct {
		key  string
		dist int
	}
	var scores []scored
	for _, key := range candidates {
		d := editDistance(target, key)
		limit := 2
		if len(target) >= 14 {
			limit = 3
		}
		if d <= limit {
			scores = append(scores, scored{key, d})
		}
	}
	if len(scores) == 0 {
		return "", false
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].dist < scores[j].dist })
	if len(scores) > 1 && scores[0].dist == scores[1].dist {
		return "", false
	}
	return scores[0].key, true
}
