package wb

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/jsonx"
)

// Salt is what the ids are derived with, so an id says nothing of the words
// it was made from. New in each process.
var Salt = func() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

// IDFor is the 32 hex digits WorkBuddy's own ids are made of, made of a name
// of the thing the id is for and the pieces that name it. The digest is that
// of the pieces joined.
func IDFor(kind string, pieces ...string) string {
	h := sha256.New()
	h.Write([]byte(Salt + "|" + kind + "|"))
	for _, p := range pieces {
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// NewMessageID is a request's own id: 32 hex digits. workbuddy2api-hub's
// 5.8.4 capture has them as UUIDv7 with the hyphens taken out (time first),
// magpie as 32 random hex digits; both are that shape, the v7 is the
// capture's.
func NewMessageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}

// uuidShape writes 32 hex digits as a UUID (version 4 bits): the
// conversation id's shape in workbuddy2api-hub's 5.8.4 capture.
func uuidShape(h string) string {
	if len(h) < 32 {
		return h
	}
	b := []byte(h[:32])
	b[12] = '4'
	switch b[16] {
	case '8', '9', 'a', 'b':
	default:
		b[16] = 'a'
	}
	s := string(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// SignatureParts is a message content's signature in the pieces it is joined
// from: its text, and a digest of every part that isn't text. A tool
// result's part is left out.
func SignatureParts(content any) []string {
	switch t := content.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, p := range t {
			o, _ := p.(*jsonx.Object)
			typ := ""
			if o != nil {
				typ = o.String("type")
			}
			if typ == "" || typ == "text" {
				if o != nil {
					if s, ok := o.Str("text"); ok && s != "" {
						out = append(out, s)
					}
				}
			} else if typ != "tool_result" && typ != "tool-result" {
				sum := sha256.Sum256(jsonx.Marshal(p))
				out = append(out, "["+typ+":"+hex.EncodeToString(sum[:])[:8]+"]")
			}
		}
		return out
	}
	return nil
}

func role(m any) string {
	if o, ok := m.(*jsonx.Object); ok {
		return o.String("role")
	}
	return ""
}

// HasBlock is whether message m's content holds a block of type.
func HasBlock(m any, typ string) bool {
	o, ok := m.(*jsonx.Object)
	if !ok {
		return false
	}
	arr, ok := o.Arr("content")
	if !ok {
		return false
	}
	for _, p := range arr {
		if po, ok := p.(*jsonx.Object); ok && po.String("type") == typ {
			return true
		}
	}
	return false
}

// AnsweredBefore is whether msgs[i] answers the message before it — a
// tool's result, or an assistant's calls — rather than carrying the user's
// own words.
func AnsweredBefore(msgs []any, i int) bool {
	if HasBlock(msgs[i], "tool_result") {
		return true
	}
	j := i - 1
	for j >= 0 && role(msgs[j]) == "system" {
		j--
	}
	if j < 0 {
		return false
	}
	prev, ok := msgs[j].(*jsonx.Object)
	if !ok {
		return false
	}
	switch prev.String("role") {
	case "tool":
		return true
	case "user":
		return HasBlock(prev, "tool_result")
	case "assistant":
		if calls, ok := prev.Arr("tool_calls"); ok && len(calls) > 0 {
			return true
		}
		return HasBlock(prev, "tool_use")
	}
	return false
}

func messagesOf(chat *jsonx.Object) []any {
	if chat == nil {
		return nil
	}
	m, _ := chat.Arr("messages")
	return m
}

// TurnParts names the user send a chat answers: the body's last user
// message that is the user's own, by its place and its signature.
func TurnParts(chat *jsonx.Object) []string {
	msgs := messagesOf(chat)
	for i := len(msgs) - 1; i >= 0; i-- {
		if role(msgs[i]) != "user" || AnsweredBefore(msgs, i) {
			continue
		}
		sig := SignatureParts(msgs[i].(*jsonx.Object).Get("content"))
		if len(sig) == 0 {
			return nil
		}
		return append([]string{"u" + itoa(i) + ":"}, sig...)
	}
	return nil
}

// ConversationParts names the conversation a chat belongs to: the session
// the client names, else the chat's first user message.
func ConversationParts(chat *jsonx.Object, session string) []string {
	if session != "" {
		return []string{"session:" + session}
	}
	for _, m := range messagesOf(chat) {
		if role(m) != "user" {
			continue
		}
		if sig := SignatureParts(m.(*jsonx.Object).Get("content")); len(sig) > 0 {
			return append([]string{"first:"}, sig...)
		}
	}
	return nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

// IDs are what Attend set, for Reattend.
type IDs struct {
	Set  []string
	Turn string
	Conv string
}

// Attend sets a chat's ids on the headers its request goes with: the
// message's (each request its own), the turn's (one user send, whatever it
// takes), and the conversation's, each the account who's own. A header the
// caller set already is kept.
func Attend(h http.Header, chat *jsonx.Object, session, who string) IDs {
	conv := ConversationParts(chat, session)
	turn := TurnParts(chat)
	ids := IDs{}
	if len(turn) > 0 {
		ids.Turn = IDFor("turn", append(append(append([]string{}, conv...), "|"), turn...)...)
	}
	if len(conv) > 0 {
		ids.Conv = IDFor("conversation", conv...)
	}
	return setIDs(h, ids, who)
}

func setIDs(h http.Header, ids IDs, who string) IDs {
	msg := NewMessageID()
	h.Set("X-Conversation-Message-ID", msg)
	h.Set("X-Request-ID", msg)
	own := func(kind, base string) string {
		if who == "" {
			return base
		}
		return IDFor(kind, "account:"+who, "|", base)
	}
	ids.Set = nil
	if h.Get("X-Conversation-Request-ID") == "" {
		turn := NewMessageID()
		if ids.Turn != "" {
			turn = own("turn", ids.Turn)
		}
		h.Set("X-Conversation-Request-ID", turn)
		ids.Set = append(ids.Set, "X-Conversation-Request-ID")
		// workbuddy2api-hub's 5.8.4 capture: the main agent's chat carries
		// X-Root-Request-ID, the same value as X-Conversation-Request-ID
		if h.Get("X-Root-Request-ID") == "" {
			h.Set("X-Root-Request-ID", turn)
			ids.Set = append(ids.Set, "X-Root-Request-ID")
		}
	}
	if ids.Conv != "" && h.Get("X-Conversation-ID") == "" {
		h.Set("X-Conversation-ID", uuidShape(own("conversation", ids.Conv)))
		ids.Set = append(ids.Set, "X-Conversation-ID")
	}
	return ids
}

// ScopeOf names the account a chat goes as for Attend.
func ScopeOf(a *Account) string {
	key := ""
	if a.Source == "key" {
		key = a.Key
	}
	return a.UID + "|" + a.EnterpriseID + "|" + key
}

// Reattend sets a chat's ids again for the account it is now sent as.
func Reattend(h http.Header, ids IDs, was, now string) IDs {
	if was == now {
		return ids
	}
	for _, k := range ids.Set {
		h.Del(k)
	}
	return setIDs(h, ids, now)
}

// sessionLimit caps a session id as magpie's are.
func sessionLimit(s string) string {
	if len(s) <= 128 {
		return s
	}
	s = s[:128]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
