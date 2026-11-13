// Package jobs ports workbuddy2api-hub's (MIT) benefit jobs — wb_tasks.py,
// wb_scheduler.py's daily cycle and wb_accounts.py's activity/daily-chat
// legs — to Go, on top of the magpie port's sign-in (package wb). Every job
// reports what it did as a Result; tasks only a real person can complete
// (the mobile App chat, following the official account, a donation …) are
// listed as pending and never faked.
package jobs

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// Hosts the China build's growth centre answers on (wb_tasks.py).
var (
	ChatBase     = "https://copilot.tencent.com"
	BillBase     = "https://www.codebuddy.cn"
	WebBase      = "https://www.workbuddy.cn"
	CampaignBase = "https://www.codebuddy.cn"
	OpenBase     = "https://open.workbuddy.cn"
	IntlBase     = "https://www.workbuddy.ai"
)

// gapScale scales every pause between upstream calls (wb2a keeps >= 1s
// between them as its anti-abuse spacing); tests set it to 0.
var gapScale atomic.Int64

func init() { gapScale.Store(1000) }

// SetGapScale sets how many milliseconds one "second" of pause lasts.
func SetGapScale(ms int64) { gapScale.Store(ms) }

func pause(ctx context.Context, seconds float64) error {
	d := time.Duration(seconds * float64(gapScale.Load()) * float64(time.Millisecond))
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Result is what one job did for one account on one run.
type Result struct {
	Job     string    `json:"job"`
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Outcome string    `json:"outcome"` // done, claimed, skipped, pending, failed …
	Credits float64   `json:"credits,omitempty"`
	Message string    `json:"message,omitempty"`
	Log     []string  `json:"log,omitempty"`
	// Pending are tasks left for a real person, with what they take.
	Pending []string `json:"pending,omitempty"`
}

func (r *Result) logf(format string, args ...any) {
	r.Log = append(r.Log, fmt.Sprintf(format, args...))
}

// Client is one account's session for the jobs.
type Client struct {
	D    wb.Doer
	Site *wb.Site
	A    *wb.Account
	// Nick is the account's display name, for the event envelopes.
	Nick string
}

// Enterprise says whether the account is an enterprise seat (no growth
// centre, as wb2a's is_enterprise).
func (c *Client) Enterprise() bool { return c.A != nil && c.A.EnterpriseID != "" }

// Key says whether the account is an API key (no programme at all).
func (c *Client) Key() bool { return c.A != nil && c.A.Source == "key" }

func (c *Client) chatBase() string {
	if c.Site != nil && c.Site.AI {
		return IntlBase
	}
	return ChatBase
}

// chatHeaders are the desktop identity's headers (wb_accounts.headers("chat")
// in its desktop profile: the same signed set the chats go with).
func (c *Client) chatHeaders() http.Header {
	h := http.Header{}
	wb.Sign(c.Site, c.A, h)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/plain, */*")
	return h
}

// billingHeaders are wb_accounts.headers("billing").
func (c *Client) billingHeaders() http.Header {
	origin := WebBase
	lang := "zh-CN"
	if c.Site.AI {
		origin, lang = IntlBase, "en-US"
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("User-Agent", webUA)
	h.Set("Origin", origin)
	h.Set("Referer", origin+"/")
	h.Set("Authorization", "Bearer "+c.A.Access)
	h.Set("X-User-Id", c.A.UID)
	h.Set("X-Domain", wb.DomainOf(c.Site, c.A))
	h.Set("X-CodeBuddy-Request", "1")
	h.Set("Accept-Language", lang)
	h.Set("X-Request-ID", RequestID())
	h.Set("X-Machine-ID", DeriveID(c.A.UID, "machine"))
	h.Set("X-Session-ID", DeriveID(c.A.UID, "session"))
	if c.A.EnterpriseID != "" {
		h.Set("X-Enterprise-Id", c.A.EnterpriseID)
		h.Set("X-Tenant-Id", c.A.EnterpriseID)
	} else {
		h.Set("X-No-Enterprise-Id", "1")
	}
	if !c.Site.AI {
		h.Set("X-Product", "SaaS")
	}
	return h
}

const webUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

// DeriveID is wb_fingerprint.derive_id: a UUID-shaped id made from the uid
// and a salt, the same every time for an account.
func DeriveID(uid, salt string) string {
	if uid == "" {
		uid = "anonymous"
	}
	sum := md5.Sum([]byte(salt + ":" + uid))
	h := hex.EncodeToString(sum[:])
	h = h[:12] + "4" + h[13:16] + "a" + h[17:]
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// RequestID is wb_fingerprint.generate_request_id: 32 hex digits.
func RequestID() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	return hex.EncodeToString(b[:])
}

// envelope is the growth centre's {code, msg, data}.
type envelope struct {
	Code json.RawMessage `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (e *envelope) ok() bool {
	c := strings.Trim(strings.TrimSpace(string(e.Code)), `"`)
	return c == "0"
}

func (e *envelope) okOr200() bool {
	c := strings.Trim(strings.TrimSpace(string(e.Code)), `"`)
	return c == "0" || c == "200"
}

func (e *envelope) codeText() string { return strings.Trim(strings.TrimSpace(string(e.Code)), `"`) }

func (e *envelope) data(out any) {
	if len(e.Data) > 0 && out != nil {
		_ = json.Unmarshal(e.Data, out)
	}
}

func (e *envelope) why() string {
	if e.Msg != "" {
		return e.Msg
	}
	return "code=" + e.codeText()
}

// call sends a JSON request and reads the envelope; status 0 is a request
// that never got an answer.
func (c *Client) call(ctx context.Context, method, url string, h http.Header, body any) (int, *envelope, error) {
	var raw []byte
	if body != nil {
		switch b := body.(type) {
		case []byte:
			raw = b
		default:
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(body); err != nil {
				return 0, nil, err
			}
			raw = bytes.TrimRight(buf.Bytes(), "\n")
		}
	}
	if h == nil {
		h = c.chatHeaders()
	}
	if raw != nil && h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := c.D.Do(cctx, &wb.Request{Method: method, URL: url, Header: h, Body: raw})
	if err != nil {
		return 0, &envelope{}, err
	}
	env := &envelope{}
	if len(bytes.TrimSpace(res.Body)) > 0 {
		if json.Unmarshal(res.Body, env) != nil {
			s := strings.TrimSpace(string(res.Body))
			if len(s) > 200 {
				s = s[:200]
			}
			env.Msg = s
		}
	}
	return res.Status, env, nil
}

// get and post are call on the chat headers.
func (c *Client) get(ctx context.Context, url string) (int, *envelope, error) {
	return c.call(ctx, http.MethodGet, url, nil, nil)
}

func (c *Client) post(ctx context.Context, url string, body any) (int, *envelope, error) {
	return c.call(ctx, http.MethodPost, url, nil, body)
}

func nowMS() int64 { return time.Now().UnixMilli() }

func uuid4() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// messageID is wb_tasks._new_message_id: the time in hex, then random hex.
func messageID() string {
	return fmt.Sprintf("%012x%s", nowMS(), strings.ReplaceAll(uuid4(), "-", "")[:20])
}

// beijing is the zone the growth centre counts days in.
var beijing = time.FixedZone("UTC+8", 8*3600)

// InNightWindow is wb_tasks.in_night_window: 23:00–08:00 Beijing.
func InNightWindow(now time.Time) bool {
	h := now.In(beijing).Hour()
	return h >= 23 || h < 8
}
