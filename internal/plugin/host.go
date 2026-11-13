// Package plugin is the CLIProxyAPI side of the port: the RPC methods CPA
// calls (plugin.register, auth.*, model.*, executor.*, quota.*,
// management.*) and the host callbacks it makes (host.http.*,
// host.stream.*, host.auth.*). The cgo exports live in package main
// (cmd/plugin), which hands this package a way to call the host.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/Sakuralaaa/WorkBuddy-plugins/internal/wb"
)

// Caller calls the host: method, a JSON-encodable payload, the result.
type Caller func(method string, payload any) (json.RawMessage, error)

var (
	callerMu sync.RWMutex
	caller   Caller
)

// SetCaller sets how this package reaches the host.
func SetCaller(c Caller) {
	callerMu.Lock()
	caller = c
	callerMu.Unlock()
}

func callHost(method string, payload any) (json.RawMessage, error) {
	callerMu.RLock()
	c := caller
	callerMu.RUnlock()
	if c == nil {
		return nil, fmt.Errorf("host is not available")
	}
	return c(method, payload)
}

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method,omitempty"`
	URL            string      `json:"url,omitempty"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type hostStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type hostStreamRead struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

// HostDoer sends requests upstream through CPA's own HTTP client (its proxy
// settings, its request log), within the host call CallbackID names; ""
// for a background job's.
type HostDoer struct{ CallbackID string }

// Do is host.http.do.
func (h HostDoer) Do(_ context.Context, req *wb.Request) (*wb.Response, error) {
	raw, err := callHost(pluginabi.MethodHostHTTPDo, hostHTTPRequest{HostCallbackID: h.CallbackID, Method: req.Method, URL: req.URL, Headers: req.Header, Body: req.Body})
	if err != nil {
		return nil, err
	}
	var res pluginapi.HTTPResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &wb.Response{Status: res.StatusCode, Header: res.Headers, Body: res.Body}, nil
}

// DoStream is host.http.do_stream, its body read through
// host.http.stream_read.
func (h HostDoer) DoStream(ctx context.Context, req *wb.Request) (*wb.StreamResponse, error) {
	raw, err := callHost(pluginabi.MethodHostHTTPDoStream, hostHTTPRequest{HostCallbackID: h.CallbackID, Method: req.Method, URL: req.URL, Headers: req.Header, Body: req.Body})
	if err != nil {
		return nil, err
	}
	var res hostStreamResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &wb.StreamResponse{Status: res.StatusCode, Header: res.Headers, Body: &hostBody{id: res.StreamID, ctx: ctx}}, nil
}

type hostBody struct {
	id     string
	ctx    context.Context
	buf    bytes.Buffer
	done   bool
	err    error
	closed bool
	mu     sync.Mutex
}

func (b *hostBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 {
		if b.err != nil {
			return 0, b.err
		}
		if b.done || b.closed {
			return 0, io.EOF
		}
		if b.ctx != nil && b.ctx.Err() != nil {
			return 0, b.ctx.Err()
		}
		raw, err := callHost(pluginabi.MethodHostHTTPStreamRead, map[string]string{"stream_id": b.id})
		if err != nil {
			b.err = err
			return 0, err
		}
		var r hostStreamRead
		if err := json.Unmarshal(raw, &r); err != nil {
			b.err = err
			return 0, err
		}
		b.buf.Write(r.Payload)
		if r.Error != "" {
			b.err = fmt.Errorf("%s", r.Error)
			b.done = true
		}
		if r.Done {
			b.done = true
		}
	}
	return b.buf.Read(p)
}

func (b *hostBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	_, _ = callHost(pluginabi.MethodHostHTTPStreamClose, map[string]string{"stream_id": b.id})
	return nil
}

// hostLog writes to CPA's log.
func hostLog(level, msg string, fields map[string]any) {
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{"level": level, "message": "[workbuddy-magpie] " + msg, "fields": fields})
}

// emit is host.stream.emit.
func emit(streamID string, payload []byte) error {
	_, err := callHost(pluginabi.MethodHostStreamEmit, map[string]any{"stream_id": streamID, "payload": payload})
	return err
}

// closeStream is host.stream.close, with a structured error when there is one.
func closeStream(streamID string, e *pluginabi.Error) {
	req := map[string]any{"stream_id": streamID}
	if e != nil {
		req["error"] = e.Message
		req["error_detail"] = e
	}
	_, _ = callHost(pluginabi.MethodHostStreamClose, req)
}

// authList is host.auth.list.
func authList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var r struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	err = json.Unmarshal(raw, &r)
	return r.Files, err
}

// authGet is host.auth.get: a credential file's JSON.
func authGet(index string) (json.RawMessage, string, error) {
	raw, err := callHost(pluginabi.MethodHostAuthGet, map[string]string{"auth_index": index})
	if err != nil {
		return nil, "", err
	}
	var r struct {
		Name string          `json:"name"`
		Path string          `json:"path"`
		JSON json.RawMessage `json:"json"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", err
	}
	name := r.Name
	if name == "" && r.Path != "" {
		name = r.Path[strings.LastIndexAny(r.Path, `/\`)+1:]
	}
	return r.JSON, name, nil
}

// authSave is host.auth.save: a credential file written to CPA's auth dir.
func authSave(name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = callHost(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{Name: name, JSON: raw})
	return err
}
