package wb

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jsonx"
)

// RefusedHint is what to do about "Illegal API invocation from an
// unapproved channel"; it ends the error's message word for word as magpie's
// Routing page has it.
const RefusedHint = "WorkBuddy refused words in this client's prompt (most often its system prompt); use it from Hermes, OpenCode or Pi, or add another provider to this group"

// RefusedDoc is where a refusal's fix is written down.
const RefusedDoc = "https://github.com/magpie-community/plugins/blob/main/packages/workbuddy/REFUSED.md"

// Refused is WorkBuddy's channel refusal.
var Refused = jsRe(`unapproved channel|illegal api invocation`, "i")

// BadSignature is WorkBuddy's refusal of a thinking block it didn't sign.
var BadSignature = jsRe("thinking_signature_invalid|invalid\\s+`?signature`?\\s+in\\s+`?thinking|block is bound to a different conversation|\"internal_code\"\\s*:\\s*\"?11159", "i")

// RestrictedHint ends a 403 code 11140's message as magpie's built-in does.
const RestrictedHint = "WorkBuddy restricted this account for unusual activity; it stays out until WorkBuddy lifts that, which an appeal (Submit feedback in WorkBuddy) asks for"

// RestrictedCode is WorkBuddy's code for a restricted account.
const RestrictedCode = 11140

var restrictedText = jsRe(`account has been restricted due to unusual activity|账号存在异常行为|帳號存在異常行為|["']code["']\s*:\s*["']?11140(?![\d.])`, "i")

// ExhaustedHint explains 14018.
const ExhaustedHint = "WorkBuddy requires an active credit package even for 0-rate free models; check your account package balance"

var exhaustedText = jsRe(`credits exhausted|额度已用尽|["']code["']\s*:\s*["']?14018(?![\d.])`, "i")

// Explained is a refusal with what to do about it said.
type Explained struct {
	Body []byte
	// Kind is "refused", "restricted", "exhausted", or "" for anything else
	// (Body then is the upstream's own text).
	Kind    string
	Message string
	Type    string
}

var innerCode = regexp.MustCompile(`^(\d{5}):`)

func codeNum(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case jsonx.Number:
		f, ok := jsonx.Num(t)
		return f, ok
	case string:
		f, ok := jsNumber(strings.TrimSpace(t))
		return f, ok
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// Explain is magpie's explained: REFUSED_HINT, RESTRICTED_HINT or
// EXHAUSTED_HINT added to that refusal's message.
func Explain(text []byte) Explained {
	s := string(text)
	v, _ := jsonx.Parse(text)
	vo := asObj(v)
	var at *obj
	if vo != nil {
		if e := vo.Obj("error"); e != nil {
			at = e
		} else {
			at = vo
		}
	}
	var codes []float64
	var inner any
	if at != nil {
		if x, ok := at.Lookup("internal_code"); ok && x != nil {
			inner = x
		}
	}
	if inner == nil && vo != nil {
		if es, ok := vo.Str("error"); ok {
			if m := innerCode.FindStringSubmatch(es); m != nil {
				inner = m[1]
			}
		}
	}
	add := func(x any) {
		if x == nil {
			return
		}
		if f, ok := codeNum(x); ok {
			codes = append(codes, f)
		} else {
			codes = append(codes, -1)
		}
	}
	add(inner)
	if at != nil {
		add(at.Obj("data").Get("code"))
		add(at.Get("code"))
	}
	if vo != nil {
		add(vo.Get("code"))
		add(vo.Obj("data").Get("code"))
	}
	has := func(c float64) bool {
		for _, x := range codes {
			if x == c {
				return true
			}
		}
		return false
	}
	isRefused := Refused.test(s)
	isRestricted := !isRefused && (has(RestrictedCode) || restrictedText.test(s))
	isExhausted := !isRestricted && !isRefused && (has(14018) || exhaustedText.test(s))
	if !isRefused && !isRestricted && !isExhausted {
		return Explained{Body: text}
	}
	msg := strings.TrimSpace(s)
	switch {
	case at != nil && isString(at.Get("message")):
		msg = at.String("message")
	case at != nil && isString(at.Obj("data").Get("msg")):
		msg = at.Obj("data").String("msg")
	case vo != nil && isString(vo.Get("error")):
		msg = vo.String("error")
	case vo != nil && vo.String("msg") != "":
		msg = vo.String("msg")
	case vo != nil && isString(vo.Obj("data").Get("msg")):
		msg = vo.Obj("data").String("msg")
	}
	hint, typ, kind := ExhaustedHint, "insufficient_quota", "exhausted"
	var code any = 14018
	switch {
	case isRefused:
		hint, typ, kind, code = RefusedHint, "permission_error", "refused", nil
	case isRestricted:
		hint, typ, kind, code = RestrictedHint, "permission_error", "restricted", RestrictedCode
	}
	where := ""
	if isRefused {
		where = RefusedDoc + " — "
	}
	msg = msg + " — " + where + hint
	body := newObj("error", newObj("message", msg, "type", typ, "code", code))
	return Explained{Body: jsonx.Marshal(body), Kind: kind, Message: msg, Type: typ}
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// ErrorMessageOf is the message a JSON error body carries, else its text.
func ErrorMessageOf(body []byte) string {
	v, _ := jsonx.Parse(body)
	if o := asObj(v); o != nil {
		if e := o.Obj("error"); e != nil {
			if m, ok := e.Str("message"); ok && m != "" {
				return m
			}
		}
		if m, ok := o.Str("msg"); ok && m != "" {
			c := ""
			if x := o.Get("code"); x != nil {
				c = " (" + jsonx.MarshalString(x) + ")"
			}
			return m + c
		}
		if m, ok := o.Str("message"); ok && m != "" {
			return m
		}
		if m, ok := o.Str("error"); ok && m != "" {
			return m
		}
	}
	t := strings.TrimSpace(string(body))
	if len(t) > 500 {
		t = t[:500]
	}
	return t
}

// resetRe reads the reset a 429 names: "your usage will reset at
// 2026-10-20 19:47:41 UTC+8", or the China build's "将在 2026-11-02
// 19:17:02 UTC+8 重置" (magpie's windowReset).
var resetRe = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})\s*(?:UTC|GMT)?\s*([+-]\d{1,2})(?::?(\d{2}))?`)

// ResetOf is the Unix time (seconds) a refusal names its reset at; 0 for none.
func ResetOf(text string) int64 {
	m := resetRe.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[3])
	mins := 0
	if m[4] != "" {
		mins, _ = strconv.Atoi(m[4])
	}
	sign := "+"
	if h < 0 {
		sign = "-"
		h = -h
	}
	zone := sign + pad2(h) + ":" + pad2(mins)
	t, err := parseTime(m[1] + "T" + m[2] + zone)
	if err != nil {
		return 0
	}
	return t
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
