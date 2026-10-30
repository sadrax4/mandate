package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func mcpServer(t *testing.T, sse bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		var result string
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"paypal","version":"1"}}`
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "s1" {
				http.Error(w, "missing session", 400)
				return
			}
			result = `{"tools":[{"name":"get_order","description":"Get an order"},{"name":"create_order","description":"Create"},{"name":"list_transactions","description":"List"}]}`
		default:
			http.Error(w, "unknown method "+req.Method, 400)
			return
		}
		msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, msg)
	}))
}

func TestProbeMCP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		srv := mcpServer(t, sse)
		tools, err := ProbeMCP(context.Background(), srv.Client(), srv.URL, "tok")
		if err != nil {
			t.Fatalf("sse=%v: %v", sse, err)
		}
		if len(tools) != 3 || tools[0].Name != "get_order" {
			t.Fatalf("sse=%v: tools = %+v", sse, tools)
		}
		if _, err := ProbeMCP(context.Background(), srv.Client(), srv.URL, "wrong"); err == nil {
			t.Fatalf("sse=%v: bad token should fail", sse)
		}
		srv.Close()
	}
}

// sseMCPServer speaks the older HTTP+SSE transport: GET /sse streams events
// (first the endpoint), POSTs to the endpoint are answered on the stream.
// /http answers 404 like PayPal's sandbox does.
func sseMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var stream http.ResponseWriter
	var flush func()
	push := func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		if stream != nil {
			fmt.Fprintf(stream, "event: message\ndata: %s\n\n", msg)
			flush()
		}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch {
		case r.URL.Path == "/http":
			http.Error(w, "Not Found", 404)
		case r.URL.Path == "/sse" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			f := w.(http.Flusher)
			mu.Lock()
			stream, flush = w, f.Flush
			mu.Unlock()
			fmt.Fprint(w, "event: endpoint\ndata: /sse/message?sessionId=abc\n\n")
			f.Flush()
			<-r.Context().Done()
			mu.Lock()
			stream = nil
			mu.Unlock()
		case r.URL.Path == "/sse/message" && r.Method == http.MethodPost:
			if r.URL.Query().Get("sessionId") != "abc" {
				http.Error(w, "bad session", 400)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(body, &req)
			w.WriteHeader(202)
			switch req.Method {
			case "initialize":
				go push(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"paypal","version":"1"}}}`, req.ID))
			case "tools/list":
				go push(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"get_order","description":"Get"},{"name":"list_transactions","description":"List"}]}}`, req.ID))
			}
		default:
			http.Error(w, "not found", 404)
		}
	}))
}

func TestProbeMCPAutoFallsBackToSSE(t *testing.T) {
	srv := sseMCPServer(t)
	defer srv.Close()
	tools, transport, err := ProbeMCPAuto(context.Background(), srv.Client(), srv.URL+"/http", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if transport != "sse" || len(tools) != 2 || tools[1].Name != "list_transactions" {
		t.Fatalf("transport=%s tools=%+v", transport, tools)
	}
	if _, _, err := ProbeMCPAuto(context.Background(), srv.Client(), srv.URL+"/http", "wrong"); err == nil {
		t.Fatal("bad token must fail")
	}
	// a Streamable HTTP server is still used directly
	s2 := mcpServer(t, true)
	defer s2.Close()
	tools, transport, err = ProbeMCPAuto(context.Background(), s2.Client(), s2.URL+"/http", "tok")
	if err != nil || transport != "streamable-http" || len(tools) != 3 {
		t.Fatalf("streamable: %v %s %d", err, transport, len(tools))
	}
}
