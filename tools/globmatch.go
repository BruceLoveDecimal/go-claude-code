package tools

import (
	"path/filepath"
	"regexp"
	"strings"
)

// globToRegexp converts a glob pattern into a regexp. It supports `*`, `?`,
// and recursive `**` path matching.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	p := filepath.ToSlash(pattern)

	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		ch := p[i]
		switch ch {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				if i+2 < len(p) && p[i+2] == '/' {
					b.WriteString(`(?:.*/)?`)
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(ch)
		case '/':
			b.WriteByte('/')
		default:
			b.WriteByte(ch)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func matchGlobPath(pattern, relPath string) bool {
	re, err := globToRegexp(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(filepath.ToSlash(relPath))
}
