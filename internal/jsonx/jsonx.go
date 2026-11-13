// Package jsonx is an order-keeping JSON model whose output is byte for byte
// what JavaScript's JSON.stringify(JSON.parse(text)) gives.
//
// magpie's WorkBuddy plugin (index.mjs) reads a chat with JSON.parse, changes
// it in place and writes it with JSON.stringify; a body nothing changed goes
// as it came. Holding to those bytes keeps this port's requests identical to
// magpie's (testdata/sent-0.1.43.json pins them by hash), which a
// map[string]any round trip through encoding/json would not: it sorts keys,
// HTML-escapes <, > and &, and spells numbers its own way.
package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Object is a JSON object that keeps its keys in JavaScript's order: array
// index keys ascending first, then the rest in insertion order.
type Object struct {
	keys []string
	vals map[string]any
}

// Number is a JSON number as written; it is printed as JavaScript prints the
// float64 it parses to.
type Number string

// NewObject is an empty object.
func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Keys are the object's keys in order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return o.orderedKeys()
}

func (o *Object) orderedKeys() []string {
	var idx []string
	var rest []string
	for _, k := range o.keys {
		if isArrayIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	if len(idx) == 0 {
		return rest
	}
	sort.Slice(idx, func(i, j int) bool {
		a, _ := strconv.ParseUint(idx[i], 10, 64)
		b, _ := strconv.ParseUint(idx[j], 10, 64)
		return a < b
	})
	return append(idx, rest...)
}

func isArrayIndex(k string) bool {
	if k == "" || len(k) > 10 {
		return false
	}
	if k != "0" && k[0] == '0' {
		return false
	}
	for _, c := range k {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 4294967295
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Has says whether key is set (to anything, null too).
func (o *Object) Has(key string) bool {
	if o == nil {
		return false
	}
	_, ok := o.vals[key]
	return ok
}

// Get is key's value, nil when unset.
func (o *Object) Get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// Lookup is key's value and whether it is set.
func (o *Object) Lookup(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// Set sets key, keeping its place when it is already there.
func (o *Object) Set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// Delete takes key out.
func (o *Object) Delete(key string) {
	if o == nil {
		return
	}
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// Clone is a shallow copy ({...o} in JavaScript).
func (o *Object) Clone() *Object {
	n := &Object{keys: append([]string(nil), o.keys...), vals: make(map[string]any, len(o.vals))}
	for k, v := range o.vals {
		n.vals[k] = v
	}
	return n
}

// Str is key's value when it is a string.
func (o *Object) Str(key string) (string, bool) {
	s, ok := o.Get(key).(string)
	return s, ok
}

// String is key's value as a string, "" when it isn't one.
func (o *Object) String(key string) string {
	s, _ := o.Str(key)
	return s
}

// Obj is key's value when it is an object.
func (o *Object) Obj(key string) *Object {
	v, _ := o.Get(key).(*Object)
	return v
}

// Arr is key's value when it is an array.
func (o *Object) Arr(key string) ([]any, bool) {
	v, ok := o.Get(key).([]any)
	return v, ok
}

// Parse reads JSON text into nil, bool, Number, string, []any and *Object.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("jsonx: trailing data")
	}
	return v, nil
}

// ParseObject reads JSON text that must be an object (or array, returned as
// a nil object and ok false).
func ParseObject(data []byte) (*Object, bool) {
	v, err := Parse(data)
	if err != nil {
		return nil, false
	}
	o, ok := v.(*Object)
	return o, ok
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return fromToken(dec, tok)
}

func fromToken(dec *json.Decoder, tok json.Token) (any, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("jsonx: object key is not a string")
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("jsonx: unexpected %v", t)
	case json.Number:
		return Number(t.String()), nil
	case string, bool, nil:
		return t, nil
	}
	return nil, fmt.Errorf("jsonx: unexpected token %T", tok)
}

// Marshal writes v as JSON.stringify would: no spaces, no HTML escaping.
func Marshal(v any) []byte {
	var b bytes.Buffer
	write(&b, v)
	return b.Bytes()
}

// MarshalString is Marshal as a string.
func MarshalString(v any) string { return string(Marshal(v)) }

func write(b *bytes.Buffer, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, t)
	case Number:
		b.WriteString(jsNumberText(string(t)))
	case float64:
		b.WriteString(FormatFloat(t))
	case float32:
		b.WriteString(FormatFloat(float64(t)))
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case uint64:
		b.WriteString(strconv.FormatUint(t, 10))
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if x == nil {
				b.WriteString("null")
				continue
			}
			write(b, x)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, x)
		}
		b.WriteByte(']')
	case *Object:
		if t == nil {
			b.WriteString("null")
			return
		}
		b.WriteByte('{')
		first := true
		for _, k := range t.orderedKeys() {
			val := t.vals[k]
			if _, skip := val.(undefined); skip {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			writeString(b, k)
			b.WriteByte(':')
			write(b, val)
		}
		b.WriteByte('}')
	case map[string]any:
		o := NewObject()
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			o.Set(k, t[k])
		}
		write(b, o)
	case json.RawMessage:
		b.Write(t)
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			b.WriteString("null")
			return
		}
		if parsed, err := Parse(raw); err == nil {
			write(b, parsed)
			return
		}
		b.Write(raw)
	}
}

type undefined struct{}

// Undefined is a value JSON.stringify leaves out of an object.
var Undefined any = undefined{}

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					fmt.Fprintf(b, `\u%04x`, c)
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// a lone byte can't be valid UTF-8; JavaScript strings would
			// have held a lone surrogate here, which it escapes
			b.WriteString(`\ufffd`)
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	b.WriteByte('"')
}

// jsNumberText is the text JavaScript gives a number literal once parsed.
func jsNumberText(raw string) string {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	return FormatFloat(f)
}

// FormatFloat formats f as JavaScript's Number.prototype.toString does.
func FormatFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		// Go: 1e+21, 1.5e-07; JavaScript: 1e+21, 1.5e-7
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		digits := strings.TrimLeft(exp[1:], "0")
		if digits == "" {
			digits = "0"
		}
		return mant + "e" + string(sign) + digits
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Num reads a JSON number (Number, float64, int) as a float64.
func Num(v any) (float64, bool) {
	switch t := v.(type) {
	case Number:
		f, err := strconv.ParseFloat(string(t), 64)
		return f, err == nil
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return 0, false
}

// Truthy is JavaScript's truthiness of a parsed JSON value.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case Number:
		f, _ := Num(t)
		return f != 0 && !math.IsNaN(f)
	case undefined:
		return false
	}
	return true
}

// Clone deep-copies a parsed value (structuredClone).
func Clone(v any) any {
	switch t := v.(type) {
	case *Object:
		if t == nil {
			return t
		}
		n := &Object{keys: append([]string(nil), t.keys...), vals: make(map[string]any, len(t.vals))}
		for k, x := range t.vals {
			n.vals[k] = Clone(x)
		}
		return n
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = Clone(x)
		}
		return out
	}
	return v
}

// Equal compares two parsed values as their JSON text (JSON.stringify(a) ===
// JSON.stringify(b)).
func Equal(a, b any) bool { return bytes.Equal(Marshal(a), Marshal(b)) }

// FromGo turns encoding/json-friendly Go values into this model.
func FromGo(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	out, err := Parse(raw)
	if err != nil {
		return nil
	}
	return out
}
