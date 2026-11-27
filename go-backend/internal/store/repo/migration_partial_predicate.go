package repo

import (
	"regexp"
	"strings"
)

var partialIndexWhere = regexp.MustCompile(`(?i)\bWHERE\b`)

// positiveIndexPredicate recognizes the exact predicate emitted by our v1-v3
// migrations, including PostgreSQL's parenthesized catalog representation.
// A substring match can mistake a narrower predicate for the intended index
// and silently leave managed listeners without uniqueness protection.
func positiveIndexPredicate(def, column string) bool {
	where := partialIndexWhere.FindStringIndex(def)
	if where == nil {
		return false
	}
	predicate := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(def[where[1]:]), ";"))
	predicate = stripIndexPredicateParens(predicate)
	parts := strings.Split(predicate, ">")
	if len(parts) != 2 {
		return false
	}
	left := strings.ToLower(stripIndexPredicateParens(parts[0]))
	right := stripIndexPredicateParens(parts[1])
	return (left == column || left == `"`+column+`"`) && right == "0"
}

func stripIndexPredicateParens(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		depth := 0
		for i, c := range s {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth < 0 || (depth == 0 && i < len(s)-1) {
				return s
			}
		}
		if depth != 0 {
			return s
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}
