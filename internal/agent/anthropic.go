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
}

// NewAnthropic builds a provider. An empty model defaults to Claude Opus 5.5.
func NewAnthropic(apiKey, model, effort string) *Anthropic {
	if model == "" {
		model = "claude-opus-5-5"
	}
	if effort == "" {
		effort = "high"
	}
	opts := []option.RequestOption{option.WithRequestTimeout(5 * time.Minute)}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	return &Anthropic{client: anthropic.NewClient(opts...), Model: model, Effort: effort, MaxTurns: 24}
}

// Name implements Provider.
func (a *Anthropic) Name() string { return "anthropic:" + a.Model }

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
			// Server-side refusal fallback: if a safety classifier declines, the
			// request is re-served by Anthropic's default fallback model.
			Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
			Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		}
		if s.a.Effort != "" {
			params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(s.a.Effort)}
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
