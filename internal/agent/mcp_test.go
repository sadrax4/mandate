package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestReadOnlyMCPTools(t *testing.T) {
	in := []string{"get_order", "create_order", "pay_order", "list_transactions", "get_refund", "create_refund",
		"get_shipment_tracking", "create_shipment_tracking", "list_disputes", "accept_dispute_claim", "send_invoice",
		"list_invoices", "get_invoice", "generate_invoice_qr_code", "update_subscription", "show_subscription_details", "list_plans"}
	got := ReadOnlyMCPTools(in)
	want := []string{"get_invoice", "get_order", "get_refund", "get_shipment_tracking", "list_disputes", "list_invoices", "list_plans", "list_transactions", "show_subscription_details"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMCPToolsetOnlyEnablesAllowedTools(t *testing.T) {
	m := &MCPServer{Name: "paypal", URL: "https://mcp.sandbox.paypal.com/http", AllowedTools: []string{"get_order", "list_transactions"},
		Token: func(context.Context) (string, error) { return "t", nil }}
	ts := mcpToolset(m)
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out["type"] != "mcp_toolset" || out["mcp_server_name"] != "paypal" {
		t.Fatalf("toolset = %s", b)
	}
	if out["default_config"].(map[string]any)["enabled"] != false {
		t.Fatalf("default must be disabled: %s", b)
	}
	cfg := out["configs"].(map[string]any)
	if len(cfg) != 2 || cfg["get_order"].(map[string]any)["enabled"] != true {
		t.Fatalf("configs = %s", b)
	}
	if _, ok := interface{}(NewAnthropic(AnthropicOptions{APIKey: "x"})).(MCPCapable); !ok {
		t.Fatal("Anthropic provider must be MCP capable")
	}
	if _, ok := interface{}(Fake{}).(MCPCapable); ok {
		t.Fatal("the scripted provider must not claim MCP support")
	}
}

func TestGatewayModeDoesNotClaimMCP(t *testing.T) {
	direct := NewAnthropic(AnthropicOptions{APIKey: "x"})
	gateway := NewAnthropic(AnthropicOptions{AuthToken: "x", BaseURL: "https://openrouter.ai/api"})
	if !direct.SupportsMCP() || gateway.SupportsMCP() {
		t.Fatalf("SupportsMCP: direct=%v gateway=%v", direct.SupportsMCP(), gateway.SupportsMCP())
	}
}
