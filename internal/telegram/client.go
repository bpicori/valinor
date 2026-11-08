// Package telegram provides a minimal Telegram Bot API HTTP client.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

const (
	// DefaultAPIEndpoint is the default Telegram Bot API endpoint.
	DefaultAPIEndpoint = "https://api.telegram.org/bot"
	// DefaultRequestTimeout is the default timeout for API requests (except long-polling).
	DefaultRequestTimeout = 30 * time.Second
	// DefaultFileTimeout is the timeout for file uploads and downloads.
	DefaultFileTimeout = 5 * time.Minute
	// DefaultLongPollTimeout is the default timeout for getUpdates long polling.
	DefaultLongPollTimeout = 30
)

// Client is a minimal Telegram Bot API HTTP client.
type Client struct {
	token          string
	httpClient     *http.Client
	fileClient     *http.Client
	longPollClient *http.Client
	endpoint       string
}

// NewClient creates a new Telegram Bot API client.
func NewClient(token string) *Client {
	return &Client{
		token:          token,
		httpClient:     &http.Client{Timeout: DefaultRequestTimeout},
		fileClient:     &http.Client{Timeout: DefaultFileTimeout},
		longPollClient: &http.Client{Timeout: 100 * time.Second}, // Max 90s poll + buffer
		endpoint:       DefaultAPIEndpoint + token + "/",
	}
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

// apiCallMultipart performs a multipart/form-data request to the Telegram Bot API.
func (c *Client) apiCallMultipart(ctx context.Context, method string, build func(*multipart.Writer) error) (*APIResponse, error) {
	url := c.endpoint + method

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := build(writer); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finalize multipart request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.fileClient.Do(req)
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

// SendPhoto uploads and sends a photo message to a chat.
func (c *Client) SendPhoto(ctx context.Context, req SendPhotoRequest) (*Message, error) {
	resp, err := c.apiCallMultipart(ctx, "sendPhoto", func(writer *multipart.Writer) error {
		if err := writer.WriteField("chat_id", fmt.Sprintf("%d", req.ChatID)); err != nil {
			return fmt.Errorf("write chat_id: %w", err)
		}
		if req.MessageThreadID != nil {
			if err := writer.WriteField("message_thread_id", fmt.Sprintf("%d", *req.MessageThreadID)); err != nil {
				return fmt.Errorf("write message_thread_id: %w", err)
			}
		}
		if req.Caption != "" {
			if err := writer.WriteField("caption", req.Caption); err != nil {
				return fmt.Errorf("write caption: %w", err)
			}
		}
		if req.ParseMode != "" {
			if err := writer.WriteField("parse_mode", req.ParseMode); err != nil {
				return fmt.Errorf("write parse_mode: %w", err)
			}
		}
		if req.ReplyParameters != nil {
			data, err := json.Marshal(req.ReplyParameters)
			if err != nil {
				return fmt.Errorf("marshal reply_parameters: %w", err)
			}
			if err := writer.WriteField("reply_parameters", string(data)); err != nil {
				return fmt.Errorf("write reply_parameters: %w", err)
			}
		}
		if req.ReplyMarkup != nil {
			data, err := json.Marshal(req.ReplyMarkup)
			if err != nil {
				return fmt.Errorf("marshal reply_markup: %w", err)
			}
			if err := writer.WriteField("reply_markup", string(data)); err != nil {
				return fmt.Errorf("write reply_markup: %w", err)
			}
		}
		if len(req.PhotoData) == 0 {
			return fmt.Errorf("photo data is required")
		}

		fileName := req.PhotoName
		if fileName == "" {
			fileName = "image"
		}

		part, err := writer.CreateFormFile("photo", fileName)
		if err != nil {
			return fmt.Errorf("create photo form file: %w", err)
		}
		if _, err := part.Write(req.PhotoData); err != nil {
			return fmt.Errorf("write photo: %w", err)
		}
		return nil
	})
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

// SendDocument uploads and sends a document to a chat.
func (c *Client) SendDocument(ctx context.Context, req SendDocumentRequest) (*Message, error) {
	resp, err := c.apiCallMultipart(ctx, "sendDocument", func(writer *multipart.Writer) error {
		if err := writer.WriteField("chat_id", fmt.Sprintf("%d", req.ChatID)); err != nil {
			return fmt.Errorf("write chat_id: %w", err)
		}
		if req.MessageThreadID != nil {
			if err := writer.WriteField("message_thread_id", fmt.Sprintf("%d", *req.MessageThreadID)); err != nil {
				return fmt.Errorf("write message_thread_id: %w", err)
			}
		}
		if req.Caption != "" {
			if err := writer.WriteField("caption", req.Caption); err != nil {
				return fmt.Errorf("write caption: %w", err)
			}
		}
		if req.ParseMode != "" {
			if err := writer.WriteField("parse_mode", req.ParseMode); err != nil {
				return fmt.Errorf("write parse_mode: %w", err)
			}
		}
		if req.ReplyMarkup != nil {
			data, err := json.Marshal(req.ReplyMarkup)
			if err != nil {
				return fmt.Errorf("marshal reply_markup: %w", err)
			}
			if err := writer.WriteField("reply_markup", string(data)); err != nil {
				return fmt.Errorf("write reply_markup: %w", err)
			}
		}
		if len(req.DocumentData) == 0 {
			return fmt.Errorf("document data is required")
		}

		fileName := req.DocumentName
		if fileName == "" {
			fileName = "file"
		}

		part, err := writer.CreateFormFile("document", fileName)
		if err != nil {
			return fmt.Errorf("create document form file: %w", err)
		}
		if _, err := part.Write(req.DocumentData); err != nil {
			return fmt.Errorf("write document: %w", err)
		}
		return nil
	})
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

// AnswerCallbackQuery acknowledges a callback query from an inline keyboard button.
func (c *Client) AnswerCallbackQuery(ctx context.Context, req AnswerCallbackQueryRequest) error {
	resp, err := c.apiCall(ctx, "answerCallbackQuery", req)
	if err != nil {
		return err
	}
	if !resp.OK {
		return resp
	}
	return nil
}

const telegramFileBaseURL = "https://api.telegram.org/file/bot"

// GetFile resolves a Telegram file_id to a download path.
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	resp, err := c.apiCall(ctx, "getFile", map[string]string{"file_id": fileID})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, resp
	}

	var file File
	if err := json.Unmarshal(resp.Result, &file); err != nil {
		return nil, fmt.Errorf("unmarshal file: %w", err)
	}
	if file.FilePath == "" {
		return nil, fmt.Errorf("telegram file has no file_path")
	}
	return &file, nil
}

// DownloadFile fetches file bytes into memory. Nothing is written to disk.
func (c *Client) DownloadFile(ctx context.Context, filePath string) ([]byte, error) {
	url := telegramFileBaseURL + c.token + "/" + filePath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	resp, err := c.fileClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download file failed (%d): %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read downloaded file: %w", err)
	}
	return data, nil
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
