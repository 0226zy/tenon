// Package openai 是 OpenAI 兼容协议的 LLM 适配器插件（插件名 "openai-compat"）。
// 与厂商的耦合被限制在本包内；重试由 application 层负责。
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0226zy/tenon/agent-runtime/internal/domain/entity"
)

// StatusError 是 HTTP 非 2xx 响应错误。
type StatusError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("模型 API 返回 %d: %s", e.StatusCode, truncate(e.Body, 300))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Retryable 判别限流、过载、超时与断连等可恢复故障。
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode == 429 || se.StatusCode >= 500
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// RetryAfterHint 提取服务端建议的等待时长，无建议时返回 0。
func RetryAfterHint(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

// Client 负责 POST {baseURL}/chat/completions。
type Client struct {
	apiKey      string
	baseURL     string
	model       string
	temperature float64
	maxTokens   int
	timeout     time.Duration
	http        *http.Client
}

// New 构造适配器。timeout 是单次调用的活性看门狗。
func New(apiKey, baseURL, model string, temperature float64, maxTokens int, timeout time.Duration) *Client {
	return &Client{
		apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), model: model,
		temperature: temperature, maxTokens: maxTokens, timeout: timeout,
		http: &http.Client{Timeout: 0},
	}
}

func (c *Client) Model() string { return c.model }

type chatRequest struct {
	Model         string                  `json:"model"`
	Messages      []entity.Message        `json:"messages"`
	Tools         []entity.ToolDefinition `json:"tools,omitempty"`
	Temperature   float64                 `json:"temperature"`
	MaxTokens     int                     `json:"max_tokens"`
	Stream        bool                    `json:"stream,omitempty"`
	StreamOptions *streamOptions          `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (c *Client) request(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, stream bool) (*http.Response, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		// 响应体由调用方读取；在请求返回时取消会误杀流，因此把 cancel 挂到 body。
		// 非流式与流式共用此逻辑，Close 必须始终由调用方执行。
		body, err := c.marshalRequest(messages, tools, stream)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := c.do(ctx, body, stream)
		if err != nil {
			cancel()
			return nil, err
		}
		resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	body, err := c.marshalRequest(messages, tools, stream)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, body, stream)
}

func (c *Client) marshalRequest(messages []entity.Message, tools []entity.ToolDefinition, stream bool) ([]byte, error) {
	req := chatRequest{Model: c.model, Messages: messages, Tools: tools,
		Temperature: c.temperature, MaxTokens: c.maxTokens, Stream: stream}
	if stream {
		req.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("编码 chat 请求失败: %w", err)
	}
	return body, nil
}

func (c *Client) do(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) && !errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %v", context.DeadlineExceeded, err)
		}
		return nil, err
	}
	return resp, nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelReadCloser) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// Chat 实现非流式 chat completion。
func (c *Client) Chat(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition) (*entity.ChatResponse, error) {
	resp, err := c.request(ctx, messages, tools, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp, payload)
	}
	var out entity.ChatResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("解析 chat 响应失败: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("chat 响应没有 choices")
	}
	out.Raw = payload
	return &out, nil
}

func statusError(resp *http.Response, payload []byte) error {
	return &StatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(payload)),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
}

type usageDTO struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage usageDTO        `json:"usage"`
	Error json.RawMessage `json:"error,omitempty"`
}

// ChatStream 消费 SSE，顺序送出文本增量，同时聚合文本和按 index 分片的工具调用。
// usage 常位于 choices 为空的独立尾帧，不能与“无 choices 的坏响应”混为一谈。
func (c *Client) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
	resp, err := c.request(ctx, messages, tools, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, readErr
		}
		return nil, statusError(resp, payload)
	}

	var content strings.Builder
	calls := map[int]*entity.ToolCall{}
	finishReason := ""
	sawChoice := false
	var out entity.ChatResponse

	consume := func(data string) (done bool, err error) {
		data = strings.TrimSpace(data)
		if data == "" {
			return false, nil
		}
		if data == "[DONE]" {
			return true, nil
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, fmt.Errorf("解析流式响应失败: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, fmt.Errorf("模型流式响应错误: %s", truncate(string(chunk.Error), 300))
		}
		if chunk.Usage != (usageDTO{}) {
			out.Usage.PromptTokens = chunk.Usage.PromptTokens
			out.Usage.CompletionTokens = chunk.Usage.CompletionTokens
			out.Usage.TotalTokens = chunk.Usage.TotalTokens
		}
		if len(chunk.Choices) == 0 {
			return false, nil
		}
		choice := chunk.Choices[0]
		sawChoice = true
		if choice.Delta.Role != "" {
			// role 在最终消息统一设置为 assistant；此处仅确认该帧属于 choice。
		}
		if choice.Delta.Content != "" {
			content.WriteString(choice.Delta.Content)
			if onDelta != nil {
				onDelta(choice.Delta.Content)
			}
		}
		for _, fragment := range choice.Delta.ToolCalls {
			call := calls[fragment.Index]
			if call == nil {
				call = &entity.ToolCall{Type: "function"}
				calls[fragment.Index] = call
			}
			if fragment.ID != "" {
				call.ID += fragment.ID
			}
			if fragment.Type != "" {
				call.Type = fragment.Type
			}
			call.Function.Name += fragment.Function.Name
			call.Function.Arguments += fragment.Function.Arguments
		}
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		return false, nil
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var dataLines []string
	done := false
	for !done && scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(dataLines) > 0 {
				done, err = consume(strings.Join(dataLines, "\n"))
				dataLines = dataLines[:0]
				if err != nil {
					return nil, err
				}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !done && len(dataLines) > 0 {
		if _, err := consume(strings.Join(dataLines, "\n")); err != nil {
			return nil, err
		}
	}
	if !sawChoice {
		return nil, errors.New("chat 流式响应没有 choices")
	}

	assistant := entity.Message{Role: "assistant", Content: content.String()}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		assistant.ToolCalls = append(assistant.ToolCalls, *calls[index])
	}
	out.Choices = append(out.Choices, struct {
		Message      entity.Message `json:"message"`
		FinishReason string         `json:"finish_reason"`
	}{Message: assistant, FinishReason: finishReason})
	return &out, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}
