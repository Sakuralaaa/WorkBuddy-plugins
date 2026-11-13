package wb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Request is an upstream HTTP request.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// Response is an upstream answer read whole.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// StreamResponse is an upstream answer whose body is read as it comes.
type StreamResponse struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// Doer sends requests upstream. In the plugin it is CPA's host HTTP client
// (host.http.do / do_stream, which applies the host's proxy and request log);
// in tests it is net/http against a fake server.
type Doer interface {
	Do(ctx context.Context, req *Request) (*Response, error)
	DoStream(ctx context.Context, req *Request) (*StreamResponse, error)
}

// NetDoer is a Doer over net/http.
type NetDoer struct{ Client *http.Client }

func (d NetDoer) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

func (d NetDoer) build(ctx context.Context, req *Request) (*http.Request, error) {
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	r, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	if ua := req.Header.Get("User-Agent"); ua == "" {
		r.Header.Set("User-Agent", "")
	}
	return r, nil
}

// Do sends req and reads the whole answer.
func (d NetDoer) Do(ctx context.Context, req *Request) (*Response, error) {
	r, err := d.build(ctx, req)
	if err != nil {
		return nil, err
	}
	res, err := d.client().Do(r)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return &Response{Status: res.StatusCode, Header: res.Header, Body: b}, nil
}

// DoStream sends req and hands back the body unread.
func (d NetDoer) DoStream(ctx context.Context, req *Request) (*StreamResponse, error) {
	r, err := d.build(ctx, req)
	if err != nil {
		return nil, err
	}
	res, err := d.client().Do(r)
	if err != nil {
		return nil, err
	}
	return &StreamResponse{Status: res.StatusCode, Header: res.Header, Body: res.Body}, nil
}

// WBError is an error WorkBuddy's API answered.
type WBError struct {
	Message string
	Code    int
	Status  int
	Said    string // as magpie's built-in (wbCall) worded it
	Bare    bool
}

func (e *WBError) Error() string { return e.Message }

// envelope is WorkBuddy's {code, msg, data}.
type envelope struct {
	Code    json.RawMessage `json:"code"`
	Msg     string          `json:"msg"`
	Message string          `json:"message"`
	Error   json.RawMessage `json:"error"`
	Data    json.RawMessage `json:"data"`
}

func codeOf(raw json.RawMessage) int {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return 0
	}
	// parseInt: leading digits
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || (end == 0 && s[end] == '-')) {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// Call makes a request of WorkBuddy's plugin API, which answers {code, msg,
// data}; a code but 0 is an error. The data goes into out when it is non-nil.
func Call(ctx context.Context, d Doer, site *Site, method, path string, headers map[string]string, body any, out any) error {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "WorkBuddy/"+site.App)
	for k, v := range headers {
		h.Set(k, v)
	}
	var b []byte
	if body != nil {
		var err error
		b, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := d.Do(ctx, &Request{Method: method, URL: site.Endpoint + path, Header: h, Body: b})
	if err != nil {
		return err
	}
	var env envelope
	_ = json.Unmarshal(res.Body, &env)
	code := codeOf(env.Code)
	if res.Status < 200 || res.Status >= 300 {
		why := env.Msg
		if why == "" {
			why = env.Message
		}
		if why == "" && len(env.Error) > 0 {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(env.Error, &e)
			why = e.Message
		}
		if why == "" {
			why = truncate(string(res.Body), 200)
		}
		if why == "" {
			why = http.StatusText(res.Status)
		}
		said := env.Msg
		if said == "" {
			if code != 0 {
				said = fmt.Sprintf("error %d", code)
			} else {
				said = StatusText(res.Status)
			}
		}
		codeNote := ""
		if code != 0 {
			codeNote = fmt.Sprintf(", code %d", code)
		}
		return &WBError{Message: fmt.Sprintf("%s: %s (HTTP %d%s)", site.Name, why, res.Status, codeNote), Code: code, Status: res.Status, Said: said}
	}
	if code != 0 {
		msg := env.Msg
		if msg == "" {
			msg = "error"
		}
		said := env.Msg
		if said == "" {
			said = fmt.Sprintf("error %d", code)
		}
		return &WBError{Message: fmt.Sprintf("%s: %s (%d)", site.Name, msg, code), Code: code, Status: res.Status, Said: said}
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// StatusText is Go's http.StatusText, or "HTTP <n>" for a status it has no
// words for, as magpie's built-in said a refused meter call.
func StatusText(n int) string {
	if s := http.StatusText(n); s != "" {
		return s
	}
	return fmt.Sprintf("HTTP %d", n)
}
