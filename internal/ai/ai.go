package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ErrNotConfigured is returned when AI features are used
// without an API key.
var ErrNotConfigured = errors.New("ai: not configured (no API key)")

const (
	defaultModel     = "claude-sonnet-4-20250514"
	defaultMaxTokens = 4096
)

// Client handles Anthropic API interactions.
type Client struct {
	apiKey string
	sdk    anthropic.Client
}

// NewClient creates a Client. If apiKey is empty, calls
// will return ErrNotConfigured.
func NewClient(apiKey string) *Client {
	c := &Client{apiKey: apiKey}
	if apiKey != "" {
		c.sdk = anthropic.NewClient(
			option.WithAPIKey(apiKey))
	}
	return c
}

// Complete sends a single-shot prompt and returns the
// response text. Used by gale search.
func (c *Client) Complete(prompt string) (string, error) {
	if c.apiKey == "" {
		return "", ErrNotConfigured
	}

	resp, err := c.sdk.Messages.New(
		context.Background(),
		anthropic.MessageNewParams{
			Model:     defaultModel,
			MaxTokens: defaultMaxTokens,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(
					anthropic.NewTextBlock(prompt)),
			},
		})
	if err != nil {
		return "", fmt.Errorf("ai: %w", err)
	}

	return extractText(resp), nil
}

// Tool defines a tool the agent can call.
type Tool struct {
	Param   anthropic.ToolParam
	Handler func(input json.RawMessage) (string, error)
}

// RunAgent executes an agentic loop with tools. Returns
// the final text response. The agent calls tools until
// it produces a text-only response or hits maxIterations.
func (c *Client) RunAgent(
	systemPrompt string,
	userPrompt string,
	tools []Tool,
	maxIterations int,
) (string, error) {
	if c.apiKey == "" {
		return "", ErrNotConfigured
	}

	// Build tool params.
	var toolParams []anthropic.ToolUnionParam
	toolMap := make(map[string]func(json.RawMessage) (string, error))
	for _, t := range tools {
		toolParams = append(toolParams,
			anthropic.ToolUnionParam{OfTool: &t.Param})
		toolMap[t.Param.Name] = t.Handler
	}

	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(
			anthropic.NewTextBlock(userPrompt)),
	}

	for i := 0; i < maxIterations; i++ {
		resp, err := c.sdk.Messages.New(
			context.Background(),
			anthropic.MessageNewParams{
				Model:     defaultModel,
				MaxTokens: defaultMaxTokens,
				System: []anthropic.TextBlockParam{
					{Text: systemPrompt},
				},
				Messages: messages,
				Tools:    toolParams,
			})
		if err != nil {
			return "", fmt.Errorf("ai: %w", err)
		}

		// Check if the response has tool use blocks.
		hasToolUse := false
		for _, block := range resp.Content {
			if block.Type == "tool_use" {
				hasToolUse = true
				break
			}
		}

		if !hasToolUse {
			return extractText(resp), nil
		}

		// Process tool calls.
		messages = append(messages, resp.ToParam())

		var toolResults []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}

			handler, ok := toolMap[block.Name]
			if !ok {
				toolResults = append(toolResults,
					anthropic.NewToolResultBlock(
						block.ID,
						fmt.Sprintf("unknown tool: %s", block.Name),
						true))
				continue
			}

			result, toolErr := handler(block.Input)
			if toolErr != nil {
				toolResults = append(toolResults,
					anthropic.NewToolResultBlock(
						block.ID, toolErr.Error(), true))
			} else {
				toolResults = append(toolResults,
					anthropic.NewToolResultBlock(
						block.ID, result, false))
			}
		}

		messages = append(messages,
			anthropic.NewUserMessage(toolResults...))
	}

	return "", fmt.Errorf("ai: agent did not converge after %d iterations", maxIterations)
}

func extractText(msg *anthropic.Message) string {
	var text string
	for _, block := range msg.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text
}
