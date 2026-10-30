package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
