package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const DefaultModel = "deepseek-v4-pro"

type Config struct {
	APIKey, BaseURL, Model string
}

func ConfigFromEnv() Config {
	c := Config{APIKey: strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")), BaseURL: strings.TrimSpace(os.Getenv("DEEPSEEK_URL")), Model: strings.TrimSpace(os.Getenv("DEEPSEEK_MODEL"))}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.deepseek.com"
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	return c
}

func (c Config) Enabled() bool { return c.APIKey != "" }

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	config  Config
	address string
	http    *http.Client
}

func NewClient(config Config) (*Client, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" {
		return nil, errors.New("DEEPSEEK_URL must be an HTTPS API URL without credentials or query parameters")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path += "/chat/completions"
	}
	return &Client{config: config, address: u.String(), http: &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Stream emits only spoken answer content, never reasoning_content.
func (c *Client) Stream(ctx context.Context, messages []Message, emit func(string) error) error {
	if !c.config.Enabled() {
		return errors.New("DeepSeek credentials are not configured")
	}
	body, err := json.Marshal(struct {
		Model     string            `json:"model"`
		Messages  []Message         `json:"messages"`
		Stream    bool              `json:"stream"`
		MaxTokens int               `json:"max_tokens"`
		Thinking  map[string]string `json:"thinking"`
	}{c.config.Model, messages, true, 512, map[string]string{"type": "disabled"}})
	if err != nil {
		return errors.New("invalid DeepSeek request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.address, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid DeepSeek endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("DeepSeek connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DeepSeek request failed (HTTP %d)", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return errors.New("DeepSeek did not return an event stream")
	}
	err = readStream(resp.Body, emit)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func readStream(reader io.Reader, emit func(string) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 128*1024)
	var data []string
	finished, content := false, false
	consume := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			if !finished || !content {
				return false, errors.New("DeepSeek returned an incomplete answer")
			}
			return true, nil
		}
		var event struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &event) != nil || len(event.Error) != 0 {
			return false, errors.New("invalid DeepSeek stream response")
		}
		for _, choice := range event.Choices {
			if choice.Delta.Content != "" {
				content = true
				if err := emit(choice.Delta.Content); err != nil {
					return false, err
				}
			}
			if choice.FinishReason != nil {
				if *choice.FinishReason != "stop" {
					return false, errors.New("DeepSeek answer stopped before completion")
				}
				finished = true
			}
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if done, err := consume(); done || err != nil {
				return err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			if len(data) > 64 {
				return errors.New("DeepSeek event is too large")
			}
		}
	}
	if scanner.Err() != nil {
		return errors.New("DeepSeek result stream disconnected")
	}
	if done, err := consume(); done || err != nil {
		return err
	}
	return errors.New("DeepSeek result stream ended without completion")
}
