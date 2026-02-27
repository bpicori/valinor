// Package telegram provides a minimal Telegram Bot API HTTP client.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultAPIEndpoint is the default Telegram Bot API endpoint.
	DefaultAPIEndpoint = "https://api.telegram.org/bot"
	// DefaultRequestTimeout is the default timeout for API requests (except long-polling).
	DefaultRequestTimeout = 30 * time.Second
	// DefaultLongPollTimeout is the default timeout for getUpdates long polling.
	DefaultLongPollTimeout = 30
)

// Client is a minimal Telegram Bot API HTTP client.
type Client struct {
	token          string
	httpClient     *http.Client
	longPollClient *http.Client
	endpoint       string
}

// NewClient creates a new Telegram Bot API client.
func NewClient(token string) *Client {
	return &Client{
		token:          token,
		httpClient:     &http.Client{Timeout: DefaultRequestTimeout},
		longPollClient: &http.Client{Timeout: 100 * time.Second}, // Max 90s poll + buffer
		endpoint:       DefaultAPIEndpoint + token + "/",
	}
}

// SetHTTPClient allows customizing the HTTP client (useful for testing).
func (c *Client) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// apiCall performs a request to the Telegram Bot API.
func (c *Client) apiCall(ctx context.Context, method string, payload interface{}) (*APIResponse, error) {
	url := c.endpoint + method

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &apiResp, nil
}

// GetUpdates fetches updates from Telegram using long polling.
// The timeout parameter specifies the maximum time for long polling (0-90 seconds).
func (c *Client) GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error) {
	url := c.endpoint + "getUpdates"
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	pollReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	pollReq.Header.Set("Content-Type", "application/json")

	resp, err := c.longPollClient.Do(pollReq)
	if err != nil {
		return nil, fmt.Errorf("execute poll: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if !apiResp.OK {
		return nil, &apiResp
	}

	var updates []Update
	if err := json.Unmarshal(apiResp.Result, &updates); err != nil {
		return nil, fmt.Errorf("unmarshal updates: %w", err)
	}

	return updates, nil
}

// SendMessage sends a text message to a chat.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (*Message, error) {
	resp, err := c.apiCall(ctx, "sendMessage", req)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, resp
	}

	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}

// EditMessageText edits a message text.
func (c *Client) EditMessageText(ctx context.Context, req EditMessageTextRequest) (*Message, error) {
	resp, err := c.apiCall(ctx, "editMessageText", req)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, resp
	}

	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}

// SendChatAction sends a chat action (e.g., typing) to a chat.
func (c *Client) SendChatAction(ctx context.Context, req SendChatActionRequest) error {
	resp, err := c.apiCall(ctx, "sendChatAction", req)
	if err != nil {
		return err
	}
	if !resp.OK {
		return resp
	}
	return nil
}

// GetMe returns basic information about the bot.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	resp, err := c.apiCall(ctx, "getMe", nil)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, resp
	}

	var user User
	if err := json.Unmarshal(resp.Result, &user); err != nil {
		return nil, fmt.Errorf("unmarshal user: %w", err)
	}

	return &user, nil
}
