// Package mcp implements an MCP (Model Context Protocol) server over stdio.
// Squad's CLI handlers are registered as tools so Claude Code can call them
// without shelling out. The wire format follows JSON-RPC 2.0 and the MCP
// spec at modelcontextprotocol.io.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ProtocolVersion advertised in the initialize handshake. Bump when the spec
// version squad targets changes; Claude Code negotiates the actual wire
// version so older clients still work. The value here must match a real MCP
// spec date — Claude Code rejects fabricated versions during the handshake.
const ProtocolVersion = "2025-06-18"

type ServerInfo struct {
	Name    string
	Version string
}

type Server struct {
	info  ServerInfo
	mu    sync.RWMutex
	tools map[string]Tool
}

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     func(ctx context.Context, args json.RawMessage) (any, error)
}

func NewServer(info ServerInfo) *Server {
	if info.Version == "" {
		info.Version = "0.0.0"
	}
	return &Server{info: info, tools: map[string]Tool{}}
}

func (s *Server) Register(t Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[t.Name] = t
}

// RestrictTo drops every registered tool whose name is not in allow. The
// auto-refine subprocess flow uses this to expose only the read tools plus
// `squad_auto_refine_apply` to the spawned claude session, even though
// registerTools has already wired the full surface.
func (s *Server) RestrictTo(allow []string) {
	if len(allow) == 0 {
		return
	}
	keep := make(map[string]struct{}, len(allow))
	for _, name := range allow {
		keep[name] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.tools {
		if _, ok := keep[name]; !ok {
			delete(s.tools, name)
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

// ToolError lets handlers return a JSON-RPC error with a non-default code.
// callTool checks errors.As(err, *ToolError) and uses Code instead of the
// catch-all errInternal. Handlers wrap an underlying error like:
//
//	return nil, &mcp.ToolError{Code: mcp.CodeInvalidParams, Err: err}
//
// for user-facing failures (missing repo, item not found, claim conflict).
type ToolError struct {
	Code int
	Err  error
}

func (e *ToolError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ToolError) Unwrap() error { return e.Err }

// CodeInvalidParams / CodeMethodNotFound are exported aliases for the
// JSON-RPC error codes squad handlers commonly map onto. Use these instead
// of hand-rolling integers in handler code.
//
// CodeNotFound (-32004) and CodeConflict (-32009) live in the
// JSON-RPC implementation-defined application-error range
// (-32000…-32099) — they are squad-specific labels, not standard
// JSON-RPC codes.
const (
	CodeInvalidParams  = errInvalidParams
	CodeMethodNotFound = errMethodNotFound
	CodeInvalidRequest = errInvalidRequest
	CodeInternal       = errInternal
	CodeNotFound       = -32004
	CodeConflict       = -32009
)

// isNotification reports whether a parsed JSON-RPC request lacks an id field
// (i.e. is a notification, which must not receive a response). json.RawMessage
// is empty when omitted, "null" when explicitly set to null; both are
// notifications per the spec.
func isNotification(id json.RawMessage) bool {
	t := bytes.TrimSpace(id)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: errParseError, Message: err.Error()},
			})
			continue
		}
		// JSON-RPC notifications carry no id field. Per spec, notifications
		// MUST NOT receive a response. Claude Code sends notifications/
		// initialized after the initialize handshake; replying with
		// "method not found" caused the client to drop the stdio transport.
		if isNotification(req.ID) {
			continue
		}
		resp := s.dispatch(ctx, req)
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) rpcResponse {
	base := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "ping":
		base.Result = map[string]any{}
		return base
	case "initialize":
		base.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    s.info.Name,
				"version": s.info.Version,
			},
		}
		return base
	case "tools/list":
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		base.Result = map[string]any{"tools": out}
		return base
	case "tools/call":
		return s.callTool(ctx, base, req.Params)
	default:
		base.Error = &rpcError{Code: errMethodNotFound, Message: "method not found: " + req.Method}
		return base
	}
}

func (s *Server) callTool(ctx context.Context, base rpcResponse, raw json.RawMessage) rpcResponse {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		base.Error = &rpcError{Code: errInvalidParams, Message: err.Error()}
		return base
	}
	s.mu.RLock()
	tool, ok := s.tools[p.Name]
	s.mu.RUnlock()
	if !ok {
		base.Error = &rpcError{Code: errMethodNotFound, Message: "tool not found: " + p.Name}
		return base
	}
	result, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		var te *ToolError
		if errors.As(err, &te) && te.Code != 0 {
			base.Error = &rpcError{Code: te.Code, Message: err.Error()}
			return base
		}
		base.Error = &rpcError{Code: errInternal, Message: err.Error()}
		return base
	}
	base.Result = map[string]any{
		"content":           []map[string]any{{"type": "text", "text": toText(result)}},
		"isError":           false,
		"structuredContent": result,
	}
	return base
}

func toText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
