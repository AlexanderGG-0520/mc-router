package scaler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type Config struct {
	Enabled bool
	URL string
	Timeout time.Duration
	Headers map[string]string
}

type Event struct {
	Backend string `json:"backend"`
	ServerAddress string `json:"serverAddress"`
	NextState int32 `json:"nextState"`
}

type Client struct {
	config Config
	httpClient *http.Client
}

func New(config Config) *Client { return &Client{config: config, httpClient: &http.Client{Timeout: config.Timeout}} }

func (c *Client) Notify(ctx context.Context, event Event) error {
	if !c.config.Enabled { return nil }
	body, err := json.Marshal(event); if err != nil { return err }
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.URL, bytes.NewReader(body)); if err != nil { return err }
	request.Header.Set("Content-Type", "application/json")
	for name, value := range c.config.Headers { request.Header.Set(name, value) }
	response, err := c.httpClient.Do(request); if err != nil { return err }
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 { return &statusError{code: response.StatusCode} }
	return nil
}

type statusError struct { code int }
func (e *statusError) Error() string { return http.StatusText(e.code) }
