package wb

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strings"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jsonx"
)

// FilterSays is the text WorkBuddy's safety filter puts in a reply it ends
// with finish_reason "content_filter".
var FilterSays = []string{"Sorry, I can't respond to this question."}

func filterSaid(text string) bool {
	t := jsTrim(strings.ReplaceAll(text, "’", "'"))
	for _, s := range FilterSays {
		if strings.HasPrefix(s, t) {
			return true
		}
	}
	return false
}

var dataLine = regexp.MustCompile(`(?m)^data:\s?(.*)$`)

func chunkOf(ev string) *obj {
	m := dataLine.FindStringSubmatch(ev)
	if m == nil || m[1] == "[DONE]" {
		return nil
	}
	v, err := jsonx.Parse([]byte(m[1]))
	if err != nil {
		return nil
	}
	return asObj(v)
}

func firstChoice(v *obj) *obj {
	if v == nil {
		return nil
	}
	arr, _ := v.Arr("choices")
	if len(arr) == 0 {
		return nil
	}
	return asObj(arr[0])
}

// Unsaid holds back a streamed reply that may still be the filter's
// refusal, and takes the refusal's text out when it ends content_filter.
type Unsaid struct {
	open bool
	said string
	held []string
}

// NewUnsaid starts a reply.
func NewUnsaid() *Unsaid { return &Unsaid{open: true} }

func without(ev string) string {
	v := chunkOf(ev)
	c := firstChoice(v)
	if c == nil {
		return ev
	}
	d := c.Obj("delta")
	if d == nil {
		return ev
	}
	if _, ok := d.Str("content"); !ok {
		return ev
	}
	d.Delete("content")
	left := 0
	for _, k := range d.Keys() {
		if k != "role" {
			left++
		}
	}
	if left == 0 && c.Get("finish_reason") == nil && v.Get("usage") == nil {
		return ""
	}
	return "data: " + jsonx.MarshalString(v)
}

// Event takes one SSE event (without its blank line) and gives the events
// to pass on now.
func (u *Unsaid) Event(ev string) []string {
	if !u.open {
		return []string{ev}
	}
	c := firstChoice(chunkOf(ev))
	text := ""
	var finish any
	var calls any
	if c != nil {
		if d := c.Obj("delta"); d != nil {
			text, _ = d.Str("content")
			calls = d.Get("tool_calls")
		}
		finish = c.Get("finish_reason")
	}
	hasCalls := false
	switch t := calls.(type) {
	case []any:
		hasCalls = len(t) > 0
	default:
		hasCalls = jsonx.Truthy(calls)
	}
	if hasCalls || (text != "" && !filterSaid(u.said+text)) {
		u.open = false
		out := append(u.held, ev)
		u.held = nil
		return out
	}
	u.said += text
	if finish == "content_filter" && u.said != "" {
		u.open = false
		var out []string
		for _, e := range append(u.held, ev) {
			if w := without(e); w != "" {
				out = append(out, w)
			}
		}
		u.held = nil
		return out
	}
	if jsonx.Truthy(finish) {
		u.open = false
		out := append(u.held, ev)
		u.held = nil
		return out
	}
	if u.said == "" {
		return []string{ev}
	}
	u.held = append(u.held, ev)
	return nil
}

// Flush gives what is held at the stream's end.
func (u *Unsaid) Flush() []string {
	out := u.held
	u.held = nil
	return out
}

// renameLine is magpie's renamed on one SSE line.
func renameLine(l string, names map[string]string) string {
	m := regexp.MustCompile(`^data:\s?(.*)$`).FindStringSubmatch(l)
	if m == nil || m[1] == "[DONE]" {
		return l
	}
	v, err := jsonx.Parse([]byte(m[1]))
	if err != nil {
		return l
	}
	if NamedBack(v, names) {
		return "data: " + jsonx.MarshalString(v)
	}
	return l
}

// ChatStream reads an upstream chat SSE stream and gives it on event by
// event with the tool names given back (renamed) and the filter's refusal
// taken out (unsaid). Each event comes without its blank line; a final
// partial event, if any, comes last.
func ChatStream(r io.Reader, names map[string]string, emit func(ev string) error) error {
	return ChatStreamRepair(r, names, nil, emit)
}

// ChatStreamRepair is ChatStream with the model's own tool-call markup, when
// it leaks into the text, turned back into tool_calls (see dsml.go).
func ChatStreamRepair(r io.Reader, names map[string]string, repair *MarkupRepair, emit func(ev string) error) error {
	if repair.Enabled() {
		ms := &markupStream{r: repair, maxIdx: -1}
		out := emit
		emit = func(ev string) error {
			for _, e := range ms.event(ev) {
				if err := out(e); err != nil {
					return err
				}
			}
			return nil
		}
		defer func() {
			for _, e := range ms.flush() {
				_ = out(e)
			}
		}()
	}
	u := NewUnsaid()
	br := bufio.NewReaderSize(r, 64*1024)
	var cur []string
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		ev := strings.Join(cur, "\n")
		cur = nil
		for _, e := range u.Event(ev) {
			if err := emit(e); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			full := strings.HasSuffix(line, "\n")
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if full && line == "" {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			} else {
				if len(names) > 0 {
					line = renameLine(line, names)
				}
				cur = append(cur, line)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	for _, e := range u.Flush() {
		if err := emit(e); err != nil {
			return err
		}
	}
	if len(cur) > 0 {
		return emit(strings.Join(cur, "\n"))
	}
	return nil
}

// SSEEvents splits an SSE stream into events (without blank lines).
func SSEEvents(r io.Reader, emit func(ev string) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var cur []string
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			full := strings.HasSuffix(line, "\n")
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if full && line == "" {
				if len(cur) > 0 {
					if e := emit(strings.Join(cur, "\n")); e != nil {
						return e
					}
					cur = nil
				}
			} else {
				cur = append(cur, line)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if len(cur) > 0 {
		return emit(strings.Join(cur, "\n"))
	}
	return nil
}

// DataOf is an SSE event's data, "" when it has none.
func DataOf(ev string) (string, bool) {
	var parts []string
	found := false
	for _, l := range strings.Split(ev, "\n") {
		if strings.HasPrefix(l, "data:") {
			found = true
			parts = append(parts, strings.TrimPrefix(strings.TrimPrefix(l, "data:"), " "))
		}
	}
	return strings.Join(parts, "\n"), found
}

// ChatCompletionOf gathers a chat SSE stream's events into the one
// chat.completion a non-streaming client asked for: WorkBuddy only streams.
func ChatCompletionOf(events []string, model string) []byte {
	type call struct {
		id, typ, name string
		args          strings.Builder
		index         int
	}
	var (
		id, created, finish  any
		content, reasoning   strings.Builder
		calls                []*call
		byIndex              = map[int]*call{}
		usage                any
		modelSeen            string
		role                 = "assistant"
		hasContent, hasThink bool
	)
	for _, ev := range events {
		data, ok := DataOf(ev)
		if !ok || data == "[DONE]" {
			continue
		}
		v, err := jsonx.Parse([]byte(data))
		if err != nil {
			continue
		}
		o := asObj(v)
		if o == nil {
			continue
		}
		if x := o.Get("id"); x != nil && id == nil {
			id = x
		}
		if x := o.Get("created"); x != nil && created == nil {
			created = x
		}
		if s, ok := o.Str("model"); ok && s != "" {
			modelSeen = s
		}
		if u := o.Get("usage"); u != nil {
			usage = u
		}
		choices, _ := o.Arr("choices")
		for _, cv := range choices {
			c := asObj(cv)
			if c == nil {
				continue
			}
			if f := c.Get("finish_reason"); f != nil {
				finish = f
			}
			d := c.Obj("delta")
			if d == nil {
				d = c.Obj("message")
			}
			if d == nil {
				continue
			}
			if s, ok := d.Str("role"); ok && s != "" {
				role = s
			}
			if s, ok := d.Str("content"); ok {
				content.WriteString(s)
				hasContent = true
			}
			for _, k := range []string{"reasoning_content", "reasoning"} {
				if s, ok := d.Str(k); ok {
					reasoning.WriteString(s)
					hasThink = true
				}
			}
			tcs, _ := d.Arr("tool_calls")
			for i, tv := range tcs {
				t := asObj(tv)
				if t == nil {
					continue
				}
				idx := i
				if n, ok := jsonx.Num(t.Get("index")); ok {
					idx = int(n)
				}
				cl, ok := byIndex[idx]
				if !ok {
					cl = &call{index: idx, typ: "function"}
					byIndex[idx] = cl
					calls = append(calls, cl)
				}
				if s, ok := t.Str("id"); ok && s != "" {
					cl.id = s
				}
				if s, ok := t.Str("type"); ok && s != "" {
					cl.typ = s
				}
				if fn := t.Obj("function"); fn != nil {
					if s, ok := fn.Str("name"); ok && s != "" {
						cl.name += s
					}
					if s, ok := fn.Str("arguments"); ok {
						cl.args.WriteString(s)
					}
				}
			}
		}
	}
	if modelSeen != "" {
		model = modelSeen
	}
	msg := newObj("role", role)
	if hasContent || len(calls) == 0 {
		msg.Set("content", content.String())
	} else {
		msg.Set("content", nil)
	}
	if hasThink {
		msg.Set("reasoning_content", reasoning.String())
	}
	if len(calls) > 0 {
		var list []any
		for _, c := range calls {
			list = append(list, newObj("id", c.id, "type", c.typ, "function", newObj("name", c.name, "arguments", c.args.String())))
		}
		msg.Set("tool_calls", list)
	}
	if id == nil {
		id = "chatcmpl-" + NewMessageID()
	}
	if created == nil {
		created = jsonx.Number("0")
	}
	if finish == nil {
		if len(calls) > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	out := newObj("id", id, "object", "chat.completion", "created", created, "model", model,
		"choices", []any{newObj("index", 0, "message", msg, "finish_reason", finish)})
	if usage != nil {
		out.Set("usage", usage)
	}
	return jsonx.Marshal(out)
}

// MessageOf gathers an Anthropic Messages SSE stream into the message a
// non-streaming client asked for.
func MessageOf(events []string) []byte {
	var msg *obj
	var blocks []*obj
	var partial = map[int]*strings.Builder{}
	for _, ev := range events {
		data, ok := DataOf(ev)
		if !ok {
			continue
		}
		v, err := jsonx.Parse([]byte(data))
		if err != nil {
			continue
		}
		o := asObj(v)
		if o == nil {
			continue
		}
		switch o.String("type") {
		case "message_start":
			msg = o.Obj("message")
		case "content_block_start":
			b := o.Obj("content_block")
			if b == nil {
				continue
			}
			idx := len(blocks)
			if n, ok := jsonx.Num(o.Get("index")); ok {
				idx = int(n)
			}
			for len(blocks) <= idx {
				blocks = append(blocks, nil)
			}
			blocks[idx] = jsonx.Clone(b).(*obj)
		case "content_block_delta":
			n, _ := jsonx.Num(o.Get("index"))
			idx := int(n)
			if idx < 0 || idx >= len(blocks) || blocks[idx] == nil {
				continue
			}
			b := blocks[idx]
			d := o.Obj("delta")
			if d == nil {
				continue
			}
			switch d.String("type") {
			case "text_delta":
				b.Set("text", b.String("text")+d.String("text"))
			case "thinking_delta":
				b.Set("thinking", b.String("thinking")+d.String("thinking"))
			case "signature_delta":
				b.Set("signature", b.String("signature")+d.String("signature"))
			case "input_json_delta":
				sb, ok := partial[idx]
				if !ok {
					sb = &strings.Builder{}
					partial[idx] = sb
				}
				sb.WriteString(d.String("partial_json"))
			}
		case "message_delta":
			if msg == nil {
				msg = jsonx.NewObject()
			}
			if d := o.Obj("delta"); d != nil {
				for _, k := range d.Keys() {
					msg.Set(k, d.Get(k))
				}
			}
			if u := o.Obj("usage"); u != nil {
				mu := msg.Obj("usage")
				if mu == nil {
					mu = jsonx.NewObject()
					msg.Set("usage", mu)
				}
				for _, k := range u.Keys() {
					mu.Set(k, u.Get(k))
				}
			}
		}
	}
	if msg == nil {
		msg = newObj("type", "message", "role", "assistant")
	}
	for idx, sb := range partial {
		if idx < len(blocks) && blocks[idx] != nil {
			if v, err := jsonx.Parse([]byte(sb.String())); err == nil {
				blocks[idx].Set("input", v)
			}
		}
	}
	var content []any
	for _, b := range blocks {
		if b != nil {
			content = append(content, b)
		}
	}
	if content == nil {
		content = []any{}
	}
	msg.Set("content", content)
	return jsonx.Marshal(msg)
}

// CollectEvents reads a whole SSE stream into its events.
func CollectEvents(r io.Reader) []string {
	var out []string
	_ = SSEEvents(r, func(ev string) error {
		out = append(out, ev)
		return nil
	})
	return out
}

// JoinEvents writes events back as an SSE stream.
func JoinEvents(events []string) []byte {
	var b bytes.Buffer
	for _, e := range events {
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.Bytes()
}
