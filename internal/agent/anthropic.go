package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
)

// Anthropic runs the agent on Claude through the official Go SDK.
type Anthropic struct {
	client   anthropic.Client
	Model    string
	Effort   string // low | medium | high | xhigh | max
	MaxTurns int
	// Compat is set when requests go through a gateway that speaks the
	// Anthropic Messages format but not every Anthropic-only extension (for
	// example OpenRouter's Anthropic-compatible endpoint). In that mode the
	// beta refusal-fallback and effort parameters are left out.
	Compat  bool
	Gateway string
}

// AnthropicOptions configures NewAnthropic.
type AnthropicOptions struct {
	APIKey    string // x-api-key auth (Anthropic Console key)
	AuthToken string // bearer auth (e.g. an OpenRouter key against its Anthropic-compatible endpoint)
	BaseURL   string // empty = https://api.anthropic.com
	Model     string // empty = claude-opus-5-5
	Effort    string // empty = high
}

// NewAnthropic builds a provider.
func NewAnthropic(o AnthropicOptions) *Anthropic {
	if o.Model == "" {
		o.Model = "claude-opus-5-5"
	}
	if o.Effort == "" {
		o.Effort = "high"
	}
	opts := []option.RequestOption{option.WithRequestTimeout(5 * time.Minute)}
	if o.APIKey != "" {
		opts = append(opts, option.WithAPIKey(o.APIKey))
	}
	if o.AuthToken != "" {
		opts = append(opts, option.WithAuthToken(o.AuthToken))
	}
	a := &Anthropic{Model: o.Model, Effort: o.Effort, MaxTurns: 24}
	if o.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimRight(o.BaseURL, "/")+"/"))
		if !strings.Contains(o.BaseURL, "api.anthropic.com") {
			a.Compat = true
			a.Gateway = o.BaseURL
		}
	}
	a.client = anthropic.NewClient(opts...)
	return a
}

// Name implements Provider.
func (a *Anthropic) Name() string {
	if a.Compat {
		return "anthropic:" + a.Model + " via gateway"
	}
	return "anthropic:" + a.Model
}

// NewSession implements Provider.
func (a *Anthropic) NewSession(system string) Session {
	return &anthropicSession{a: a, system: system}
}

type anthropicSession struct {
	a       *Anthropic
	system  string
	history []anthropic.BetaMessageParam
}

func (s *anthropicSession) Send(ctx context.Context, userText string, tools []Tool, emit func(Event)) (string, error) {
	s.history = append(s.history, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(userText)))
	byName := map[string]Tool{}
	betaTools := make([]anthropic.BetaToolUnionParam, 0, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		betaTools = append(betaTools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: t.Properties, Required: t.Required},
		}})
	}
	var finalText strings.Builder
	for turn := 0; turn < s.a.MaxTurns; turn++ {
		params := anthropic.BetaMessageNewParams{
			Model:     s.a.Model,
			MaxTokens: 8000,
			System:    []anthropic.BetaTextBlockParam{{Text: s.system, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}},
			Messages:  s.history,
			Tools:     betaTools,
		}
		if !s.a.Compat {
			// Server-side refusal fallback: if a safety classifier declines, the
			// request is re-served by Anthropic's default fallback model.
			params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
			params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
			if s.a.Effort != "" {
				params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(s.a.Effort)}
			}
		}
		resp, err := s.a.client.Beta.Messages.New(ctx, params)
		if err != nil {
			return "", describeErr(err)
		}
		s.history = append(s.history, resp.ToParam())

		var results []anthropic.BetaContentBlockParamUnion
		for _, block := range resp.Content {
			switch v := block.AsAny().(type) {
			case anthropic.BetaTextBlock:
				if strings.TrimSpace(v.Text) != "" {
					finalText.Reset()
					finalText.WriteString(v.Text)
					emit(Event{Type: "assistant", Message: v.Text})
				}
			case anthropic.BetaToolUseBlock:
				raw := json.RawMessage(v.JSON.Input.Raw())
				emit(Event{Type: "tool_call", Message: v.Name, Data: map[string]any{"tool": v.Name, "input": raw}})
				tool, ok := byName[v.Name]
				if !ok {
					results = append(results, anthropic.NewBetaToolResultBlock(v.ID, "unknown tool "+v.Name, true))
					continue
				}
				out, err := tool.Run(ctx, raw)
				if err != nil {
					emit(Event{Type: "tool_result", Message: v.Name + ": " + err.Error(), Data: map[string]any{"tool": v.Name, "error": err.Error()}})
					results = append(results, anthropic.NewBetaToolResultBlock(v.ID, "error: "+err.Error(), true))
					continue
				}
				emit(Event{Type: "tool_result", Message: v.Name + " ok", Data: map[string]any{"tool": v.Name, "bytes": len(out)}})
				results = append(results, anthropic.NewBetaToolResultBlock(v.ID, out, false))
			}
		}
		switch resp.StopReason {
		case anthropic.BetaStopReasonToolUse:
			if len(results) == 0 {
				return finalText.String(), errors.New("model requested tools but produced no tool calls")
			}
			s.history = append(s.history, anthropic.NewBetaUserMessage(results...))
			continue
		case anthropic.BetaStopReasonRefusal:
			return "", fmt.Errorf("the model declined this request (%s)", resp.StopDetails.Explanation)
		case anthropic.BetaStopReasonMaxTokens:
			return finalText.String(), errors.New("the model ran out of output tokens")
		default:
			return finalText.String(), nil
		}
	}
	return finalText.String(), fmt.Errorf("agent stopped after %d turns without finishing", s.a.MaxTurns)
}

func describeErr(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return errors.New("Anthropic API key rejected (401): check ANTHROPIC_API_KEY")
		case 429:
			return errors.New("Anthropic rate limit hit (429): try again in a moment")
		case 400:
			return fmt.Errorf("Anthropic rejected the request (400): %s", apiErr.Error())
		default:
			return fmt.Errorf("Anthropic API error (%d): %s", apiErr.StatusCode, apiErr.Error())
		}
	}
	return fmt.Errorf("Anthropic request failed: %w", err)
}
