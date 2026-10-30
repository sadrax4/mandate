package agent

import (
	"context"
	"regexp"
	"sort"
)

// MCPServer describes a remote MCP server the model may call through the
// Anthropic MCP connector: the API calls the server on the model's behalf, so
// the tools never run inside this process.
type MCPServer struct {
	Name string
	URL  string
	// Token returns the bearer token the connector presents to the server.
	Token func(ctx context.Context) (string, error)
	// AllowedTools is the explicit list of tools the model may call. Every
	// other tool the server offers stays disabled.
	AllowedTools []string
}

// MCPCapable is implemented by providers that can attach MCP tools to a
// session (the Anthropic provider; the scripted offline agent cannot).
type MCPCapable interface {
	NewMCPSession(system string, mcp MCPServer) Session
}

// readOnlyVerb matches tool names whose verb is a read: get_order,
// list_transactions, show_subscription_details. The verb decides; the noun
// may well be "refund" or "dispute", which are fine to look at.
var readOnlyVerb = regexp.MustCompile(`^(get|list|search|show|fetch|read|lookup)_[a-z0-9_]+$`)

// ReadOnlyMCPTools keeps the tools whose names are reads and drops anything
// that could move money or change state (create_*, pay_*, create_refund,
// accept_dispute_claim, send_invoice, ...). It is the allowlist Mandate
// applies to PayPal's MCP toolset: the agent may look at orders,
// transactions, tracking, refunds, disputes and invoices, never create or
// pay anything.
func ReadOnlyMCPTools(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if readOnlyVerb.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
