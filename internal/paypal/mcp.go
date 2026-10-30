package paypal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// PayPal's hosted MCP servers (Streamable HTTP transport). They expose the
// PayPal Agent Toolkit (orders, invoices, subscriptions, disputes, shipment
// tracking, transaction reporting) to any MCP client.
const (
	MCPSandboxURL = "https://mcp.sandbox.paypal.com/http"
	MCPLiveURL    = "https://mcp.paypal.com/http"
)

// MCPURL returns the PayPal MCP endpoint matching this client's environment.
func (r *REST) MCPURL() string {
	if r.Mode() == "live" {
		return MCPLiveURL
	}
	return MCPSandboxURL
}

// AccessToken exposes the cached OAuth2 bearer token so other PayPal surfaces
// (the MCP server) can be called with the same credentials.
func (r *REST) AccessToken(ctx context.Context) (string, error) { return r.accessToken(ctx) }

// MCPTool is one entry of an MCP tools/list result.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ProbeMCP performs the MCP handshake (initialize, notifications/initialized,
// tools/list) against a Streamable HTTP endpoint and returns the tools it
// offers. It is how Mandate checks at startup that PayPal's MCP server
// accepts the merchant credentials before the model is pointed at it.
func ProbeMCP(ctx context.Context, hc *http.Client, url, token string) ([]MCPTool, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	c := &mcpClient{hc: hc, url: url, token: token}
	return c.handshake(ctx)
}

// ProbeMCPAuto tries the Streamable HTTP endpoint first and, when the server
// does not serve it (PayPal's sandbox answers 404 on /http), falls back to
// the older HTTP+SSE transport on the sibling /sse endpoint. It returns the
// tools and the transport that worked ("streamable-http" or "sse").
func ProbeMCPAuto(ctx context.Context, hc *http.Client, url, token string) ([]MCPTool, string, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 25 * time.Second}
	}
	if strings.HasSuffix(url, "/sse") {
		tools, err := ProbeMCPSSE(ctx, hc, url, token)
		return tools, "sse", err
	}
	tools, err := ProbeMCP(ctx, hc, url, token)
	var apiErr *APIError
	if err != nil && errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 405) {
		sseURL := strings.TrimSuffix(strings.TrimSuffix(url, "/http"), "/") + "/sse"
		tools, err2 := ProbeMCPSSE(ctx, hc, sseURL, token)
		if err2 == nil {
			return tools, "sse", nil
		}
		return nil, "", fmt.Errorf("%v; sse: %w", err, err2)
	}
	return tools, "streamable-http", err
}

// ProbeMCPSSE performs the handshake over the HTTP+SSE transport: a GET on
// the /sse URL opens an event stream whose first event names the endpoint to
// POST JSON-RPC requests to; responses arrive on the stream.
func ProbeMCPSSE(ctx context.Context, hc *http.Client, sseURL, token string) ([]MCPTool, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 25 * time.Second}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sse connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	stream := &sseReader{r: bufio.NewReaderSize(resp.Body, 64*1024)}
	ev, data, err := stream.next()
	if err != nil {
		return nil, fmt.Errorf("sse: no endpoint event: %w", err)
	}
	if ev != "endpoint" {
		return nil, fmt.Errorf("sse: expected endpoint event, got %q", ev)
	}
	base, err := neturl.Parse(sseURL)
	if err != nil {
		return nil, err
	}
	epRef, err := neturl.Parse(strings.TrimSpace(data))
	if err != nil {
		return nil, fmt.Errorf("sse: bad endpoint %q", data)
	}
	endpoint := base.ResolveReference(epRef).String()
	c := &mcpClient{hc: hc, url: endpoint, token: token}

	// Requests are POSTed to the endpoint; replies come back on the stream.
	call := func(id int, method string, params any) (json.RawMessage, error) {
		resp, err := c.post(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		}
		want, _ := json.Marshal(id)
		// Some servers answer inline instead of on the stream.
		if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") && len(body) > 0 {
			var r rpcResponse
			if json.Unmarshal(body, &r) == nil && bytes.Equal(r.ID, want) {
				return r.unwrap()
			}
		}
		for {
			ev, data, err := stream.next()
			if err != nil {
				return nil, fmt.Errorf("sse: waiting for %s reply: %w", method, err)
			}
			if ev != "message" && ev != "" {
				continue
			}
			var r rpcResponse
			if json.Unmarshal([]byte(data), &r) == nil && bytes.Equal(r.ID, want) {
				return r.unwrap()
			}
		}
	}
	init := map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "mandate", "version": "1.0"}}
	if _, err := call(1, "initialize", init); err != nil {
		return nil, fmt.Errorf("mcp initialize (sse): %w", err)
	}
	_ = c.notify(ctx, "notifications/initialized")
	res, err := call(2, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list (sse): %w", err)
	}
	var out struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("mcp tools/list: decode: %w", err)
	}
	return out.Tools, nil
}

// sseReader yields (event, data) pairs from a text/event-stream body.
type sseReader struct{ r *bufio.Reader }

func (s *sseReader) next() (string, string, error) {
	var ev string
	var data []string
	for {
		line, err := s.r.ReadString('\n')
		if err != nil && line == "" {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if len(data) > 0 || ev != "" {
				return ev, strings.Join(data, "\n"), nil
			}
		case strings.HasPrefix(line, ":"): // keepalive comment
		case strings.HasPrefix(line, "event:"):
			ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if err != nil {
			if len(data) > 0 {
				return ev, strings.Join(data, "\n"), nil
			}
			return "", "", err
		}
	}
}

func (c *mcpClient) handshake(ctx context.Context) ([]MCPTool, error) {
	init := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mandate", "version": "1.0"},
	}
	if _, err := c.call(ctx, 1, "initialize", init); err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	_ = c.notify(ctx, "notifications/initialized")
	res, err := c.call(ctx, 2, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	var out struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("mcp tools/list: decode: %w", err)
	}
	return out.Tools, nil
}

type mcpClient struct {
	hc        *http.Client
	url       string
	token     string
	sessionID string
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *mcpClient) post(ctx context.Context, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	return resp, nil
}

func (c *mcpClient) notify(ctx context.Context, method string) error {
	resp, err := c.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// call sends one JSON-RPC request and returns its result, reading either a
// plain JSON body or a text/event-stream carrying the response.
func (c *mcpClient) call(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	resp, err := c.post(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	want, _ := json.Marshal(id)
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case line == "":
				if len(data) > 0 {
					var r rpcResponse
					if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &r); err == nil && bytes.Equal(r.ID, want) {
						return r.unwrap()
					}
					data = nil
				}
			}
		}
		if len(data) > 0 {
			var r rpcResponse
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &r); err == nil && bytes.Equal(r.ID, want) {
				return r.unwrap()
			}
		}
		return nil, errors.New("no response for request in event stream")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var r rpcResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode: %w (%s)", err, truncateBody(body))
	}
	return r.unwrap()
}

func (r rpcResponse) unwrap() (json.RawMessage, error) {
	if r.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", r.Error.Code, r.Error.Message)
	}
	return r.Result, nil
}

func truncateBody(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
