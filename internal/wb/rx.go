package wb

import (
	"unicode/utf16"

	"github.com/dlclark/regexp2"
)

// rx is a JavaScript regular expression: magpie's patterns use lookbehind
// and lookahead, which Go's regexp hasn't, so they run on regexp2 in its
// ECMAScript mode (\w is ASCII, as in JavaScript).
type rx struct{ re *regexp2.Regexp }

func jsRe(pattern string, flags string) *rx {
	opt := regexp2.RegexOptions(regexp2.ECMAScript)
	for _, f := range flags {
		switch f {
		case 'i':
			opt |= regexp2.IgnoreCase
		case 'm':
			opt |= regexp2.Multiline
		}
	}
	return &rx{re: regexp2.MustCompile(pattern, opt)}
}

// replaceAll is text.replace(re, to) for a /g pattern; to may use $1.
func (r *rx) replaceAll(text, to string) string {
	out, err := r.re.Replace(text, to, -1, -1)
	if err != nil {
		return text
	}
	return out
}

// replaceFunc is text.replace(re, fn) for a /g pattern.
func (r *rx) replaceFunc(text string, fn func(m regexp2.Match) string) string {
	out, err := r.re.ReplaceFunc(text, func(m regexp2.Match) string { return fn(m) }, -1, -1)
	if err != nil {
		return text
	}
	return out
}

// test is re.test(text).
func (r *rx) test(text string) bool {
	ok, err := r.re.MatchString(text)
	return err == nil && ok
}

// findSubmatch is the first match's groups, nil for none.
func (r *rx) findSubmatch(text string) []string {
	m, err := r.re.FindStringMatch(text)
	if err != nil || m == nil {
		return nil
	}
	gs := m.Groups()
	out := make([]string, len(gs))
	for i, g := range gs {
		if len(g.Captures) > 0 {
			out[i] = g.String()
		}
	}
	return out
}

// jsLen is a string's length as JavaScript counts it, in UTF-16 units.
func jsLen(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

var _ = utf16.IsSurrogate
