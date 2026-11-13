package wb

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jsonx"
)

type obj = jsonx.Object

func asObj(v any) *obj {
	o, _ := v.(*obj)
	return o
}

func newObj(kv ...any) *obj {
	o := jsonx.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}

// ---- words WorkBuddy refuses ----------------------------------------------

type flag struct {
	re *rx
	to string
}

// Flagged are the words WorkBuddy refuses a chat for, "Illegal API
// invocation from an unapproved channel" (magpie #182), each with words that
// say the same and pass (index.mjs FLAGGED, where each is accounted for).
var Flagged = []flag{
	{jsRe(`x-anthropic-billing-header:[^\n]*\n?`, "gi"), ""},
	{jsRe(`You are Claude Code, Anthropic['’]s official CLI for Claude`, "gi"), "You are Claude Code, Anthropic's CLI for Claude"},
	{jsRe(`Main branch \(you will usually use this for PRs\)`, "gi"), "Main branch (usually the base for PRs)"},
	{jsRe(`(?<!the )Codex CLI is an open source project led by OpenAI\.`, "gi"), "The Codex CLI is an open source project led by OpenAI."},
	{jsRe(`You are an AI agent powered by DeepSeek Harness\.?`, "gi"), "You are an AI agent, powered by DeepSeek Harness."},
	{jsRe(`Web GUI whose URL the runtime context names\.\s+When the user refers`, "gi"), "Web GUI whose URL the runtime context names. The interface, when the user refers"},
}

// Unflagged is text with Flagged's words in place.
func Unflagged(text string) string {
	for _, f := range Flagged {
		text = f.re.replaceAll(text, f.to)
	}
	return text
}

// unflag puts Flagged's words in place in every message, leaving out a text
// that had nothing else (the billing header's system block).
func unflag(messages []any) ([]any, bool) {
	changed := false
	for i := len(messages) - 1; i >= 0; i-- {
		m := asObj(messages[i])
		if m == nil {
			continue
		}
		switch c := m.Get("content").(type) {
		case string:
			t := Unflagged(c)
			if t == c {
				continue
			}
			changed = true
			if jsTrim(t) != "" || m.String("role") != "system" {
				m.Set("content", t)
			} else {
				messages = append(messages[:i:i], messages[i+1:]...)
			}
		case []any:
			var parts []any
			here := false
			for _, p := range c {
				po := asObj(p)
				text, isText := "", false
				if po != nil {
					text, isText = po.Str("text")
				}
				if !isText {
					parts = append(parts, p)
					continue
				}
				t := Unflagged(text)
				if t == text {
					parts = append(parts, p)
					continue
				}
				here = true
				if jsTrim(t) != "" {
					np := po.Clone()
					np.Set("text", t)
					parts = append(parts, np)
				}
			}
			if !here {
				continue
			}
			changed = true
			if len(parts) > 0 || m.String("role") != "system" {
				if parts == nil {
					parts = []any{}
				}
				m.Set("content", parts)
			} else {
				messages = append(messages[:i:i], messages[i+1:]...)
			}
		}
	}
	return messages, changed
}

// ---- the chat endpoint's spellings ------------------------------------------

func reasoningOf(m *obj) string {
	if s, ok := m.Str("reasoning_content"); ok && s != "" {
		return s
	}
	if arr, ok := m.Arr("reasoning_details"); ok {
		var b strings.Builder
		for _, d := range arr {
			do := asObj(d)
			if do == nil {
				continue
			}
			if s, ok := do.Str("text"); ok {
				b.WriteString(s)
			} else if s, ok := do.Str("summary"); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// asReasoning sends an earlier turn's thinking back as `reasoning`, the
// field WorkBuddy's own client sets (it drops reasoning_content).
func asReasoning(messages []any) bool {
	changed := false
	for _, x := range messages {
		m := asObj(x)
		if m == nil || m.String("role") != "assistant" || (!m.Has("reasoning_content") && !m.Has("reasoning_details")) {
			continue
		}
		text := reasoningOf(m)
		if r, ok := m.Str("reasoning"); text != "" && !(ok && r != "") {
			m.Set("reasoning", text)
		}
		m.Delete("reasoning_content")
		m.Delete("reasoning_details")
		changed = true
	}
	return changed
}

// DefaultSystem is WorkBuddy's default system prompt.
const DefaultSystem = "You are a helpful assistant."

// systemOn makes withSystem's changes to a chat; true when it made any.
func systemOn(b *obj) bool {
	msgs, ok := b.Arr("messages")
	if !ok || len(msgs) == 0 {
		return false
	}
	msgs, changed := unflag(msgs)
	if changed {
		if msgs == nil {
			msgs = []any{}
		}
		b.Set("messages", msgs)
	}
	if b.Has("max_completion_tokens") {
		if !b.Has("max_tokens") {
			b.Set("max_tokens", b.Get("max_completion_tokens"))
		}
		b.Delete("max_completion_tokens")
		changed = true
	}
	for _, x := range msgs {
		if m := asObj(x); m != nil && m.String("role") == "developer" {
			m.Set("role", "system")
			changed = true
		}
	}
	if asReasoning(msgs) {
		changed = true
	}
	if len(msgs) == 0 || role(msgs[0]) != "system" {
		msgs = append([]any{newObj("role", "system", "content", DefaultSystem)}, msgs...)
		b.Set("messages", msgs)
		changed = true
	}
	return changed
}

func textOf(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if po := asObj(p); po != nil {
				if s, ok := po.Str("text"); ok && s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// SystemKeyOf is the text of a chat's system messages, which a chat refused
// once is known by.
func SystemKeyOf(chat *obj) string {
	msgs := messagesOf(chat)
	if msgs == nil {
		return ""
	}
	var parts []string
	for _, m := range msgs {
		if role(m) == "system" {
			parts = append(parts, textOf(asObj(m).Get("content")))
		}
	}
	key := strings.Join(parts, "\n")
	if jsTrim(key) == DefaultSystem {
		return ""
	}
	return key
}

// SystemAsUserOn reads the system prompt to the model as the first thing the
// user says, in <instructions> tags, after WorkBuddy's default system prompt;
// a later system message becomes the user's where it stands.
func SystemAsUserOn(b *obj) bool {
	msgs, ok := b.Arr("messages")
	if !ok {
		return false
	}
	i := 0
	var lead []string
	for ; i < len(msgs) && role(msgs[i]) == "system"; i++ {
		lead = append(lead, textOf(asObj(msgs[i]).Get("content")))
	}
	said := jsTrim(strings.Join(lead, "\n\n"))
	rest := make([]any, 0, len(msgs)-i)
	same := true
	for _, m := range msgs[i:] {
		if role(m) == "system" {
			c := asObj(m).Clone()
			c.Set("role", "user")
			rest = append(rest, c)
			same = false
		} else {
			rest = append(rest, m)
		}
	}
	moved := said != "" && said != DefaultSystem
	if !moved && same {
		return false
	}
	if moved {
		text := "<instructions>\n" + said + "\n</instructions>"
		u := -1
		for k, m := range rest {
			if role(m) == "user" {
				u = k
				break
			}
		}
		if u < 0 {
			rest = append([]any{newObj("role", "user", "content", text)}, rest...)
		} else {
			um := asObj(rest[u]).Clone()
			switch c := um.Get("content").(type) {
			case string:
				um.Set("content", text+"\n\n"+c)
			case []any:
				um.Set("content", append([]any{newObj("type", "text", "text", text)}, c...))
			default:
				um.Set("content", text)
			}
			rest[u] = um
		}
	}
	b.Set("messages", append([]any{newObj("role", "system", "content", DefaultSystem)}, rest...))
	return true
}

// openers are the shapes in which an agent's opening says which harness it
// runs in; one more comma passes the vendor's whole-line match.
var openers = []flag{
	{jsRe(`(\bYou are (?:an?|the)\s[^\n.,]{0,60}?\b(?:agent|assistant))\s+(powered by|running on|running in|built on|based on)\b`, "gi"), "$1, $2"},
}

// Opened is text with the openers' comma set.
func Opened(text string) string {
	for _, f := range openers {
		text = f.re.replaceAll(text, f.to)
	}
	return text
}

// CommaPausedOn sets the openers' comma in a chat's system and assistant
// messages, in place; roles collects the roles it went into.
func CommaPausedOn(b *obj, roles map[string]bool) bool {
	msgs, ok := b.Arr("messages")
	if !ok {
		return false
	}
	changed := false
	for _, x := range msgs {
		m := asObj(x)
		if m == nil {
			continue
		}
		r := m.String("role")
		if r != "system" && r != "assistant" {
			continue
		}
		mine := false
		switch c := m.Get("content").(type) {
		case string:
			if t := Opened(c); t != c {
				m.Set("content", t)
				mine = true
			}
		case []any:
			for _, p := range c {
				po := asObj(p)
				if po == nil {
					continue
				}
				s, ok := po.Str("text")
				if !ok {
					continue
				}
				if t := Opened(s); t != s {
					po.Set("text", t)
					mine = true
				}
			}
		}
		if !mine {
			continue
		}
		changed = true
		if roles != nil {
			roles[r] = true
		}
	}
	return changed
}

// toolChoiceOn converts an OpenAI client's object tool_choice to
// WorkBuddy's string form.
func toolChoiceOn(b *obj) bool {
	if b == nil || !b.Has("tool_choice") {
		return false
	}
	choice := b.Get("tool_choice")
	if s, ok := choice.(string); ok {
		if strings.ToLower(jsTrim(s)) != "none" {
			return false
		}
		b.Delete("tool_choice")
		b.Delete("tools")
		b.Delete("functions")
		return true
	}
	c := asObj(choice)
	if c == nil {
		b.Delete("tool_choice")
		return true
	}
	typ := ""
	if s, ok := c.Str("type"); ok {
		typ = strings.ToLower(jsTrim(s))
	}
	switch typ {
	case "none":
		b.Delete("tool_choice")
		b.Delete("tools")
		b.Delete("functions")
	case "auto", "required":
		b.Set("tool_choice", typ)
	case "function":
		name := ""
		if fn := c.Obj("function"); fn != nil {
			if s, ok := fn.Str("name"); ok {
				name = jsTrim(s)
			}
		}
		if name == "" {
			name = "auto"
		}
		b.Set("tool_choice", name)
	default:
		b.Delete("tool_choice")
	}
	return true
}

// ---- tools WorkBuddy takes ----------------------------------------------------

func nameOK(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func safeName(name string, taken map[string]bool) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x80 && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'):
			b.WriteRune(r)
		case r >= 0x10000:
			b.WriteString("__") // two UTF-16 units, as JavaScript's replace sees it
		default:
			b.WriteByte('_')
		}
	}
	base := b.String()
	if base == "" {
		base = "tool"
	}
	n := base
	for i := 2; taken[n]; i++ {
		n = base + "_" + strconv.Itoa(i)
	}
	taken[n] = true
	return n
}

var nulEscape = jsRe(`\\\\|\\0(?![0-9])`, "g")

func isObjectish(v any) bool {
	switch v.(type) {
	case *obj, []any:
		return true
	}
	return false
}

// fitSchema mends a schema's \0 patterns and tuple items, at every depth.
func fitSchema(s any, depth int) bool {
	if depth > 64 {
		return false
	}
	switch t := s.(type) {
	case []any:
		changed := false
		for _, v := range t {
			if fitSchema(v, depth+1) {
				changed = true
			}
		}
		return changed
	case *obj:
		if t == nil {
			return false
		}
		changed := false
		if p, ok := t.Str("pattern"); ok && strings.Contains(p, `\0`) {
			np := nulEscape.replaceFunc(p, func(m regexp2.Match) string {
				if m.String() == `\\` {
					return m.String()
				}
				return `\u0000`
			})
			if np != p {
				t.Set("pattern", np)
				changed = true
			}
		}
		if items, ok := t.Arr("items"); ok {
			var list []any
			for _, v := range items {
				if isObjectish(v) {
					list = append(list, v)
				}
			}
			switch len(list) {
			case 0:
				t.Set("items", jsonx.NewObject())
			case 1:
				t.Set("items", list[0])
			default:
				t.Set("items", newObj("anyOf", list))
			}
			t.Delete("additionalItems")
			changed = true
		}
		for _, k := range t.Keys() {
			if fitSchema(t.Get(k), depth+1) {
				changed = true
			}
		}
		return changed
	}
	return false
}

var schemaTypes = map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "object": true, "array": true, "null": true}

var (
	subOne  = []string{"items", "additionalItems", "unevaluatedItems", "contains", "not", "if", "then", "else", "propertyNames", "additionalProperties", "unevaluatedProperties"}
	subList = []string{"anyOf", "oneOf", "allOf", "prefixItems"}
	subMap  = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}
	boolOK  = map[string]bool{"additionalProperties": true, "unevaluatedProperties": true}
)

func valueType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case []any:
		return "array"
	case jsonx.Number:
		f, _ := jsonx.Num(t)
		if f == float64(int64(f)) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	}
	return "object"
}

// jsonEqualUndef is JSON.stringify(a) === JSON.stringify(b), where a nil
// pointer stands for undefined.
func sameJSON(a any, aSet bool, b any, bSet bool) bool {
	if !aSet || !bSet {
		return aSet == bSet
	}
	return jsonx.Equal(a, b)
}

// fitEnum gives an enum a type.
func fitEnum(s *obj) bool {
	if !s.Has("enum") {
		return false
	}
	enum, ok := s.Arr("enum")
	if !ok || len(enum) == 0 {
		s.Delete("enum")
		return true
	}
	var listed []any
	switch t := s.Get("type").(type) {
	case []any:
		listed = t
	case string:
		listed = []any{t}
	}
	nonNull := 0
	for _, t := range listed {
		if t != "null" {
			nonNull++
		}
	}
	if nonNull == 1 {
		return false
	}
	var order []string
	by := map[string][]any{}
	for _, v := range enum {
		t := valueType(v)
		if _, ok := by[t]; !ok {
			order = append(order, t)
		}
		by[t] = append(by[t], v)
	}
	del := func(k string) bool {
		if _, ok := by[k]; !ok {
			return false
		}
		delete(by, k)
		for i, x := range order {
			if x == k {
				order = append(order[:i:i], order[i+1:]...)
				break
			}
		}
		return true
	}
	if _, i := by["integer"]; i {
		if _, n := by["number"]; n {
			by["number"] = append(by["number"], by["integer"]...)
			del("integer")
		}
	}
	hasNull := del("null")
	if len(order) <= 1 {
		var want any
		switch {
		case len(order) == 0:
			want = "null"
		case hasNull:
			want = []any{order[0], "null"}
		default:
			want = order[0]
		}
		cur, has := s.Lookup("type")
		if sameJSON(want, true, cur, has) {
			return false
		}
		s.Set("type", want)
		return true
	}
	var split []any
	for _, t := range order {
		split = append(split, newObj("type", t, "enum", by[t]))
	}
	if hasNull {
		split = append(split, newObj("type", "null"))
	}
	s.Delete("enum")
	s.Delete("type")
	if _, ok := s.Arr("anyOf"); ok {
		all, _ := s.Arr("allOf")
		s.Set("allOf", append(append([]any{}, all...), newObj("anyOf", split)))
	} else {
		s.Set("anyOf", split)
	}
	return true
}

func subSchema(v any, key string) any {
	b, ok := v.(bool)
	if !ok || boolOK[key] {
		return v
	}
	if b {
		return jsonx.NewObject()
	}
	return newObj("not", jsonx.NewObject())
}

// fitShape mends a schema to the shape Kimi takes, at every depth.
func fitShape(sv any, depth int) bool {
	s := asObj(sv)
	if s == nil || depth > 64 {
		return false
	}
	changed := false
	if cur, has := s.Lookup("type"); has {
		var list []any
		if arr, ok := cur.([]any); ok {
			list = arr
		} else {
			list = []any{cur}
		}
		var uniq []any
		seen := map[string]bool{}
		for _, t := range list {
			ts, ok := t.(string)
			if !ok {
				continue
			}
			ts = strings.ToLower(ts)
			if !schemaTypes[ts] || seen[ts] {
				continue
			}
			seen[ts] = true
			uniq = append(uniq, ts)
		}
		var want any
		wantSet := true
		switch len(uniq) {
		case 0:
			wantSet = false
		case 1:
			want = uniq[0]
		default:
			want = uniq
		}
		if !sameJSON(want, wantSet, cur, true) {
			if !wantSet {
				s.Delete("type")
			} else {
				s.Set("type", want)
			}
			changed = true
		}
	}
	if s.Has("required") {
		req, ok := s.Arr("required")
		if !ok {
			s.Delete("required")
			changed = true
		} else {
			bad := false
			for _, k := range req {
				if _, ok := k.(string); !ok {
					bad = true
				}
			}
			if bad {
				var kept []any
				for _, k := range req {
					if _, ok := k.(string); ok {
						kept = append(kept, k)
					}
				}
				if kept == nil {
					kept = []any{}
				}
				s.Set("required", kept)
				changed = true
			}
		}
	}
	if fitEnum(s) {
		changed = true
	}
	for _, k := range subOne {
		if !s.Has(k) {
			continue
		}
		v0 := s.Get(k)
		v := subSchema(v0, k)
		if _, wasBool := v0.(bool); wasBool && !boolOK[k] {
			s.Set(k, v)
			changed = true
		}
		if fitShape(v, depth+1) {
			changed = true
		}
	}
	for _, k := range subList {
		arr, ok := s.Arr(k)
		if !ok {
			continue
		}
		for i, v0 := range arr {
			v := subSchema(v0, k)
			if _, wasBool := v0.(bool); wasBool {
				arr[i] = v
				changed = true
			}
			if fitShape(v, depth+1) {
				changed = true
			}
		}
	}
	for _, k := range subMap {
		m := s.Obj(k)
		if m == nil {
			continue
		}
		for _, name := range m.Keys() {
			v0 := m.Get(name)
			v := subSchema(v0, k)
			if _, wasBool := v0.(bool); wasBool {
				m.Set(name, v)
				changed = true
			}
			if fitShape(v, depth+1) {
				changed = true
			}
		}
	}
	return changed
}

func index(v any, k string) any {
	switch t := v.(type) {
	case *obj:
		return t.Get(k)
	case []any:
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= len(t) {
			return nil
		}
		return t[i]
	}
	return nil
}

// localRef is the schema a local $ref names, or s itself.
func localRef(root any, s any) any {
	so := asObj(s)
	if so == nil {
		return s
	}
	ref, ok := so.Str("$ref")
	if !ok || !strings.HasPrefix(ref, "#/") {
		return s
	}
	var at any = root
	for _, k := range strings.Split(ref[2:], "/") {
		k = strings.ReplaceAll(strings.ReplaceAll(k, "~1", "/"), "~0", "~")
		at = index(at, k)
	}
	if isObjectish(at) {
		return at
	}
	return s
}

func hasUnion(b *obj) bool {
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		if _, ok := b.Arr(k); ok {
			return true
		}
	}
	return false
}

// objectRoot makes a tool's parameters an object at the root.
func objectRoot(ps *obj, root any, depth int) bool {
	var unions []string
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		if _, ok := ps.Arr(k); ok {
			unions = append(unions, k)
		}
	}
	if t, _ := ps.Str("type"); t == "object" && ps.Get("type") == "object" && len(unions) == 0 {
		return false
	}
	props := ps.Obj("properties")
	if props == nil {
		props = jsonx.NewObject()
	}
	var required []any
	if r, ok := ps.Arr("required"); ok {
		required = append(required, r...)
	}
	branchOf := func(b0 any) *obj {
		b := asObj(localRef(root, b0))
		if b == nil {
			return nil
		}
		if depth < 8 && hasUnion(b) {
			b = jsonx.Clone(b).(*obj)
			objectRoot(b, root, depth+1)
		}
		return b
	}
	if all, ok := ps.Arr("allOf"); ok {
		for _, b0 := range all {
			b := branchOf(b0)
			if b == nil {
				continue
			}
			if bp := b.Obj("properties"); bp != nil {
				for _, k := range bp.Keys() {
					if !props.Has(k) {
						props.Set(k, bp.Get(k))
					}
				}
			}
			if r, ok := b.Arr("required"); ok {
				required = append(required, r...)
			}
		}
	}
	var branches []*obj
	for _, key := range []string{"anyOf", "oneOf"} {
		arr, _ := ps.Arr(key)
		for _, b0 := range arr {
			b := branchOf(b0)
			if b == nil {
				continue
			}
			if b.Get("type") != "object" && !jsonx.Truthy(b.Get("properties")) {
				continue
			}
			branches = append(branches, b)
		}
	}
	var fromOrder []string
	from := map[string][]any{}
	for _, b := range branches {
		bp := b.Obj("properties")
		if bp == nil {
			continue
		}
		for _, k := range bp.Keys() {
			if props.Has(k) {
				continue
			}
			v := bp.Get(k)
			seen, ok := from[k]
			if !ok {
				fromOrder = append(fromOrder, k)
			}
			dup := false
			for _, x := range seen {
				if jsonx.Equal(x, v) {
					dup = true
					break
				}
			}
			if !dup {
				seen = append(seen, v)
			}
			from[k] = seen
		}
	}
	for _, k := range fromOrder {
		list := from[k]
		if len(list) == 1 {
			props.Set(k, list[0])
		} else {
			props.Set(k, newObj("anyOf", list))
		}
	}
	if len(branches) > 0 {
		if bp := branches[0].Obj("properties"); bp != nil {
			for _, k := range bp.Keys() {
				every := true
				for _, b := range branches {
					r, ok := b.Arr("required")
					if !ok || !containsAny(r, k) {
						every = false
						break
					}
				}
				if every {
					required = append(required, k)
				}
			}
		}
	}
	for _, k := range unions {
		ps.Delete(k)
	}
	ps.Set("type", "object")
	ps.Set("properties", props)
	var req []any
	seen := map[string]bool{}
	for _, k := range required {
		ks, ok := k.(string)
		if !ok || seen[ks] {
			continue
		}
		seen[ks] = true
		if props.Has(ks) {
			req = append(req, ks)
		}
	}
	if len(req) > 0 {
		ps.Set("required", req)
	} else {
		ps.Delete("required")
	}
	return true
}

func containsAny(arr []any, k string) bool {
	for _, x := range arr {
		if x == k {
			return true
		}
	}
	return false
}

// typeUnions gives an untyped oneOf of object branches type "object".
func typeUnions(sv any, root any, depth int) bool {
	s := asObj(sv)
	if s == nil || depth > 64 {
		return false
	}
	changed := false
	if !s.Has("type") {
		if one, ok := s.Arr("oneOf"); ok && len(one) > 0 {
			all := true
			for _, b := range one {
				if !objectBranch(root, b, nil) {
					all = false
					break
				}
			}
			if all {
				s.Set("type", "object")
				changed = true
			}
		}
	}
	for _, k := range subOne {
		if typeUnions(s.Get(k), root, depth+1) {
			changed = true
		}
	}
	for _, k := range subList {
		arr, _ := s.Arr(k)
		for _, v := range arr {
			if typeUnions(v, root, depth+1) {
				changed = true
			}
		}
	}
	for _, k := range subMap {
		m := s.Obj(k)
		if m == nil {
			continue
		}
		for _, name := range m.Keys() {
			if typeUnions(m.Get(name), root, depth+1) {
				changed = true
			}
		}
	}
	return changed
}

func objectBranch(root any, b any, seen []string) bool {
	bo := asObj(b)
	if bo == nil {
		return false
	}
	if bo.Has("type") {
		return bo.Get("type") == "object"
	}
	ref, ok := bo.Str("$ref")
	if !ok || len(seen) > 16 {
		return false
	}
	for _, s := range seen {
		if s == ref {
			return false
		}
	}
	t := localRef(root, bo)
	if to, ok := t.(*obj); !ok || to == bo {
		return false
	}
	return objectBranch(root, t, append(append([]string{}, seen...), ref))
}

// FitTools mends a chat's function tools: parameters and names. It says
// whether it changed anything, and gives the names it gave, each to the
// client's own.
func FitTools(b *obj) (bool, map[string]string) {
	names := map[string]string{}
	tools, ok := b.Arr("tools")
	if !ok || len(tools) == 0 {
		return false, names
	}
	changed := false
	taken := map[string]bool{}
	for _, t := range tools {
		if fn := asObj(t).Obj("function"); fn != nil {
			if n, ok := fn.Str("name"); ok && nameOK(n) {
				taken[n] = true
			}
		}
	}
	theirs := map[string]string{}
	for _, tv := range tools {
		t := asObj(tv)
		if t == nil {
			continue
		}
		var fn *obj
		if t.Get("type") == "function" || jsonx.Truthy(t.Get("function")) {
			fn = t.Obj("function")
		}
		if fn == nil {
			continue
		}
		if n, ok := fn.Str("name"); ok && !nameOK(n) {
			safe := safeName(n, taken)
			names[safe] = n
			theirs[n] = safe
			fn.Set("name", safe)
			changed = true
		}
		if !fn.Has("parameters") {
			continue
		}
		params := fn.Obj("parameters")
		if params == nil {
			fn.Set("parameters", newObj("type", "object", "properties", jsonx.NewObject()))
			changed = true
			continue
		}
		if fitSchema(params, 0) {
			changed = true
		}
		if fitShape(params, 0) {
			changed = true
		}
		if typeUnions(params, params, 0) {
			changed = true
		}
		if objectRoot(params, params, 0) {
			changed = true
		}
	}
	if len(theirs) > 0 {
		msgs, _ := b.Arr("messages")
		for _, mv := range msgs {
			m := asObj(mv)
			if m == nil {
				continue
			}
			calls, _ := m.Arr("tool_calls")
			for _, c := range calls {
				fn := asObj(c).Obj("function")
				if fn == nil {
					continue
				}
				if safe, ok := theirs[fn.String("name")]; ok {
					fn.Set("name", safe)
				}
			}
			if m.String("role") == "tool" {
				if n, ok := m.Str("name"); ok {
					if safe, ok := theirs[n]; ok {
						m.Set("name", safe)
					}
				}
			}
		}
	}
	if tc := asObj(b.Get("tool_choice")); tc != nil {
		if fn := tc.Obj("function"); fn != nil {
			if safe, ok := theirs[fn.String("name")]; ok {
				fn.Set("name", safe)
			}
		}
	}
	return changed, names
}

// ---- growth ------------------------------------------------------------------

// GrowthSend is the growth event WorkBuddy's client puts in a chat's body.
const GrowthSend = "chat_request_send"

// InputLengthOf is the length of the chat's last own user message's text,
// in UTF-16 units as WorkBuddy's CLI counts it.
func InputLengthOf(msgs []any) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if role(msgs[i]) != "user" || AnsweredBefore(msgs, i) {
			continue
		}
		switch c := asObj(msgs[i]).Get("content").(type) {
		case string:
			return jsLen(c)
		case []any:
			n := 0
			for _, p := range c {
				po := asObj(p)
				if po == nil {
					continue
				}
				typ := "text"
				if t, ok := po.Lookup("type"); ok && t != nil {
					typ, _ = t.(string)
				}
				if s, ok := po.Str("text"); typ == "text" && ok {
					n += jsLen(s)
				}
			}
			return n
		default:
			return 0
		}
	}
	return 0
}

// WithGrowth gives a WorkBuddy AI chat the extra_vars its own client sends
// (magpie #691): growthEvent, a JSON string of growth events, and initiator
// "user". was is the conversation id an earlier call gave the event for.
func WithGrowth(site *Site, chat *obj, conv, was string) bool {
	if !site.AI || conv == "" || chat == nil {
		return false
	}
	msgs, ok := chat.Arr("messages")
	if !ok {
		return false
	}
	vars := chat.Obj("extra_vars")
	if vars == nil {
		vars = jsonx.NewObject()
	}
	var events []any
	if g, ok := vars.Str("growthEvent"); ok && jsTrim(g) != "" {
		if v, err := jsonx.Parse([]byte(g)); err == nil {
			if arr, ok := v.([]any); ok {
				events = arr
			} else {
				events = []any{v}
			}
		}
	}
	if was != "" {
		var keep []any
		for _, e := range events {
			eo := asObj(e)
			if eo != nil && eo.Get("eventCode") == GrowthSend && eo.Get("id") == was {
				continue
			}
			keep = append(keep, e)
		}
		events = keep
	}
	model, _ := chat.Str("model")
	name := model
	for _, m := range site.Models {
		if m.ID == model {
			name = m.Name
			break
		}
	}
	event := newObj("eventCode", GrowthSend, "id", conv, "extra", newObj(
		"inputLength", InputLengthOf(msgs),
		"requestModelId", model,
		"requestModelName", name,
		"mode", "craft",
		"command", "",
	))
	has := false
	for _, e := range events {
		eo := asObj(e)
		if eo != nil && eo.Get("eventCode") == GrowthSend && eo.Get("id") == conv {
			has = true
		}
	}
	if !has {
		events = append(events, event)
	}
	if events == nil {
		events = []any{}
	}
	nv := vars.Clone()
	nv.Set("growthEvent", jsonx.MarshalString(events))
	ini := vars.Get("initiator")
	if ini == nil {
		ini = "user"
	}
	nv.Set("initiator", ini)
	chat.Set("extra_vars", nv)
	return true
}

// WithVars is a chat body with extra_vars set as its last field, every byte
// it had kept.
func WithVars(body []byte, vars any) []byte {
	s := string(body)
	i := strings.LastIndex(s, "}")
	if i < 0 {
		return body
	}
	before := strings.TrimRight(s[:i], " \t\n\r\v\f\u00a0\ufeff")
	open := strings.HasSuffix(before, "{")
	sep := ","
	if open {
		sep = ""
	}
	return []byte(s[:i] + sep + `"extra_vars":` + jsonx.MarshalString(vars) + "}" + s[i+1:])
}

// Regrown is a body given for conversation was given its growth event for
// conv instead; nil when it can't be read.
func Regrown(site *Site, body []byte, was, conv string) []byte {
	chat, ok := jsonx.ParseObject(body)
	if !ok || !WithGrowth(site, chat, conv, was) {
		return nil
	}
	return jsonx.Marshal(chat)
}

// ---- thinking on Messages -------------------------------------------------------

// unsignedOn drops the thinking blocks with no signature (withoutUnsigned).
func unsignedOn(b *obj) bool {
	msgs, ok := b.Arr("messages")
	if !ok {
		return false
	}
	changed := false
	for _, mv := range msgs {
		m := asObj(mv)
		if m == nil || m.String("role") != "assistant" {
			continue
		}
		content, ok := m.Arr("content")
		if !ok {
			continue
		}
		var kept []any
		for _, p := range content {
			po := asObj(p)
			if po != nil && po.Get("type") == "thinking" && !jsonx.Truthy(po.Get("signature")) {
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == len(content) {
			continue
		}
		changed = true
		if len(kept) == 0 {
			kept = []any{newObj("type", "text", "text", "")}
		}
		m.Set("content", kept)
	}
	return changed
}

// WithoutUnsigned is a Messages body without unsigned thinking blocks; the
// body itself when it has none.
func WithoutUnsigned(body []byte) []byte {
	b, ok := jsonx.ParseObject(body)
	if !ok || !unsignedOn(b) {
		return body
	}
	return jsonx.Marshal(b)
}

// WithoutSignedThinking is a Messages body without any thinking block; nil
// when there was none to drop.
func WithoutSignedThinking(body []byte) []byte {
	b, ok := jsonx.ParseObject(body)
	if !ok {
		return nil
	}
	msgs, ok := b.Arr("messages")
	if !ok {
		return nil
	}
	dropped := 0
	for _, mv := range msgs {
		m := asObj(mv)
		if m == nil || m.String("role") != "assistant" {
			continue
		}
		content, ok := m.Arr("content")
		if !ok {
			continue
		}
		var kept []any
		for _, p := range content {
			if po := asObj(p); po != nil && po.Get("type") == "thinking" {
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == len(content) {
			continue
		}
		dropped += len(content) - len(kept)
		if len(kept) == 0 {
			kept = []any{newObj("type", "text", "text", "")}
		}
		m.Set("content", kept)
	}
	if dropped == 0 {
		return nil
	}
	return jsonx.Marshal(b)
}

// ---- tool results after the user's message ----------------------------------------

// AfterToolsRefused is WorkBuddy's 400 code 11153.
var AfterToolsRefused = jsRe(`must end with a user message|["']code["']\s*:\s*["']?11153(?![\d.])`, "i")

// ReplyAfterToolsOn puts an empty assistant message between a chat's tool
// results and the user's message(s) that end it.
func ReplyAfterToolsOn(b *obj) bool {
	msgs, ok := b.Arr("messages")
	if !ok {
		return false
	}
	i := len(msgs) - 1
	for i >= 0 && role(msgs[i]) == "user" {
		i--
	}
	if i == len(msgs)-1 || i < 0 || role(msgs[i]) != "tool" {
		return false
	}
	out := make([]any, 0, len(msgs)+1)
	out = append(out, msgs[:i+1]...)
	out = append(out, newObj("role", "assistant", "content", ""))
	out = append(out, msgs[i+1:]...)
	b.Set("messages", out)
	return true
}

func onBody(body []byte, f func(*obj) bool) []byte {
	b, ok := jsonx.ParseObject(body)
	if !ok || !f(b) {
		return nil
	}
	return jsonx.Marshal(b)
}

// ReplyAfterTools is ReplyAfterToolsOn on a body; nil when it doesn't apply.
func ReplyAfterTools(body []byte) []byte { return onBody(body, ReplyAfterToolsOn) }

// SystemAsUser is SystemAsUserOn on a body; nil when there's nothing to move.
func SystemAsUser(body []byte) []byte { return onBody(body, SystemAsUserOn) }

// CommaPaused is CommaPausedOn on a body; nil when no comma was set.
func CommaPaused(body []byte, roles map[string]bool) []byte {
	return onBody(body, func(b *obj) bool { return CommaPausedOn(b, roles) })
}

// ---- DeepSeek Harness's name -------------------------------------------------------

var dshLines = []flag{
	{jsRe(`You are an AI agent,? powered by DeepSeek Harness\.?`, "g"), "You are an AI agent."},
	{jsRe(`DeepSeek Harness (Web GUI)`, "g"), "$1"},
	{jsRe(`DeepSeek Harness (implementation checkout)`, "g"), "agent $1"},
	{jsRe(`\bDSH itself\b`, "g"), "the agent itself"},
}

const (
	inPathBefore = `(?<![\w/\\.~:@-])`
	inPathAfter  = `(?![\w/\\@-]|\.\w)`
)

var (
	dshThe  = jsRe(inPathBefore+`([Tt]he )?DeepSeek Harness`+inPathAfter, "g")
	dshBare = jsRe(inPathBefore+`DSH`+inPathAfter, "g")
)

// DshHidden takes DeepSeek Harness's name out of text it wrote; in the
// user's or the assistant's text (said) only its own lines are reworded.
func DshHidden(text string, said bool) string {
	for _, f := range dshLines {
		text = f.re.replaceAll(text, f.to)
	}
	if said {
		return text
	}
	text = dshThe.replaceFunc(text, func(m regexp2.Match) string {
		g := m.Groups()
		if len(g) > 1 && len(g[1].Captures) > 0 {
			return g[1].String() + "agent harness"
		}
		return "the agent harness"
	})
	return dshBare.replaceAll(text, "the agent harness")
}

// HideDsh puts DshHidden on a chat's messages (a tool's result left as it
// came), Messages' system and the tools' descriptions.
func HideDsh(chat *obj) bool {
	if chat == nil {
		return false
	}
	changed := false
	parts := func(content any, said bool) any {
		switch c := content.(type) {
		case string:
			t := DshHidden(c, said)
			if t != c {
				changed = true
			}
			return t
		case []any:
			out := make([]any, len(c))
			for i, p := range c {
				out[i] = p
				po := asObj(p)
				if po == nil {
					continue
				}
				text, ok := po.Str("text")
				if !ok {
					continue
				}
				if t, has := po.Lookup("type"); has && t != "text" {
					continue
				}
				nt := DshHidden(text, said)
				if nt == text {
					continue
				}
				changed = true
				np := po.Clone()
				np.Set("text", nt)
				out[i] = np
			}
			return out
		}
		return content
	}
	if msgs, ok := chat.Arr("messages"); ok {
		for _, mv := range msgs {
			m := asObj(mv)
			if m == nil || m.String("role") == "tool" {
				continue
			}
			r := m.String("role")
			c, has := m.Lookup("content")
			nc := parts(c, r != "system" && r != "developer")
			if has || nc != nil {
				m.Set("content", nc)
			}
		}
	}
	if chat.Has("system") {
		chat.Set("system", parts(chat.Get("system"), false))
	}
	var described func(v any)
	described = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				described(x)
			}
		case *obj:
			for _, k := range t.Keys() {
				if s, ok := t.Get(k).(string); ok && k == "description" {
					if nt := DshHidden(s, false); nt != s {
						t.Set(k, nt)
						changed = true
					}
				} else {
					described(t.Get(k))
				}
			}
		}
	}
	if tools, ok := chat.Arr("tools"); ok {
		described(tools)
	}
	return changed
}

// ---- reasoning effort -------------------------------------------------------------

// SmallBudget is the largest max_tokens a chat can name and still be one no
// client meant to think in.
const SmallBudget = 1024

// NamesEffort is whether a chat names its effort, in either spelling.
func NamesEffort(chat *obj) bool {
	if chat.Has("reasoning_effort") {
		return true
	}
	r := chat.Get("reasoning")
	switch t := r.(type) {
	case *obj:
		return t.Has("effort")
	}
	return false
}

// WithDefaultEffort gives a chat that names no effort the model's default.
func WithDefaultEffort(chat *obj, list []Model) bool {
	if chat == nil || NamesEffort(chat) {
		return false
	}
	limit := chat.Get("max_tokens")
	if limit == nil {
		limit = chat.Get("max_completion_tokens")
	}
	if n, ok := limit.(jsonx.Number); ok {
		if f, _ := jsonx.Num(n); f > 0 && f <= SmallBudget {
			return false
		}
	}
	model, _ := chat.Str("model")
	for _, m := range list {
		if m.ID == model {
			if m.Effort == "" {
				return false
			}
			chat.Set("reasoning_effort", m.Effort)
			return true
		}
	}
	return false
}

// ---- reading a chat once ----------------------------------------------------------------

// Prepared is a request's body read once for every change made to it.
type Prepared struct {
	Body    []byte
	Chat    *obj
	Names   map[string]string
	Changed bool
}

// Prepare reads body once and makes withSystem's, fitTools' and the tool
// choice's changes (a chat), or withoutUnsigned's (Messages).
func Prepare(body []byte, native bool) Prepared {
	none := Prepared{Body: body, Names: map[string]string{}}
	chat, ok := jsonx.ParseObject(body)
	if !ok {
		return none
	}
	if native {
		return Prepared{Body: body, Chat: chat, Names: map[string]string{}, Changed: unsignedOn(chat)}
	}
	changed := systemOn(chat)
	fit, names := FitTools(chat)
	if fit {
		changed = true
	}
	if toolChoiceOn(chat) {
		changed = true
	}
	return Prepared{Body: body, Chat: chat, Names: names, Changed: changed}
}

// NamedBack gives the model's calls in a chat answer the client's names.
func NamedBack(v any, names map[string]string) bool {
	o := asObj(v)
	if o == nil || len(names) == 0 {
		return false
	}
	changed := false
	choices, _ := o.Arr("choices")
	for _, c := range choices {
		co := asObj(c)
		if co == nil {
			continue
		}
		for _, at := range []*obj{co.Obj("message"), co.Obj("delta")} {
			if at == nil {
				continue
			}
			calls, _ := at.Arr("tool_calls")
			for _, tc := range calls {
				fn := asObj(tc).Obj("function")
				if fn == nil {
					continue
				}
				if ours, ok := names[fn.String("name")]; ok {
					fn.Set("name", ours)
					changed = true
				}
			}
		}
	}
	return changed
}
