// Package pitchmentor is the Go SDK for the Pitch Mentor API (https://api.pitchmentor.net).
//
//	client := pitchmentor.NewClient(os.Getenv("PITCHMENTOR_API_KEY"))
//	for call, err := range client.ListCallsIter(ctx, &pitchmentor.ListCallsParams{}) { ... }
package pitchmentor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	Version        = "0.1.0"
	DefaultBaseURL = "https://api.pitchmentor.net"
	maxRetryDelay  = 60 * time.Second
)

// Page is one page of a list endpoint. Pass NextCursor as Cursor for the next one, or use the …Iter methods.
type Page[T any] struct {
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// Client calls the Pitch Mentor API. Create it with NewClient.
type Client struct {
	apiKey     string
	BaseURL    string
	HTTPClient *http.Client
	// MaxRetries for 429, 5xx and network errors (honoring retry-after). Default 2.
	MaxRetries int
	sleep      func(ctx context.Context, d time.Duration) error
}

// Option configures a Client.
type Option func(*Client)

func WithBaseURL(u string) Option          { return func(c *Client) { c.BaseURL = strings.TrimRight(u, "/") } }
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.HTTPClient = h } }
func WithMaxRetries(n int) Option          { return func(c *Client) { c.MaxRetries = n } }

// NewClient creates a client. An empty apiKey falls back to PITCHMENTOR_API_KEY, and the base URL
// to PITCHMENTOR_BASE_URL, else https://api.pitchmentor.net.
func NewClient(apiKey string, opts ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv("PITCHMENTOR_API_KEY")
	}
	base := os.Getenv("PITCHMENTOR_BASE_URL")
	if base == "" {
		base = DefaultBaseURL
	}
	c := &Client{apiKey: apiKey, BaseURL: strings.TrimRight(base, "/"), HTTPClient: &http.Client{Timeout: 60 * time.Second}, MaxRetries: 2, sleep: sleepCtx}
	for _, o := range opts {
		o(c)
	}
	return c
}

type requestConfig struct {
	idempotencyKey string
	headers        http.Header
}

// RequestOption customizes a single request.
type RequestOption func(*requestConfig)

// WithIdempotencyKey sets the Idempotency-Key of a POST (otherwise one is generated and reused across retries).
func WithIdempotencyKey(key string) RequestOption {
	return func(r *requestConfig) { r.idempotencyKey = key }
}

// WithHeader adds a request header.
func WithHeader(key, value string) RequestOption {
	return func(r *requestConfig) { r.headers.Add(key, value) }
}

// Error is any non-2xx response. Code is the API's machine-readable error code (e.g. "not_found").
type Error struct {
	Status  int
	Code    string
	Message string
	Header  http.Header
	Body    []byte
}

func (e *Error) Error() string {
	return fmt.Sprintf("pitchmentor: %d %s: %s", e.Status, e.Code, e.Message)
}

// RetryAfter is the server's retry-after hint (zero if absent).
func (e *Error) RetryAfter() time.Duration { return parseRetryAfter(e.Header.Get("Retry-After")) }

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool { return statusOf(err) == http.StatusNotFound }

// IsRateLimited reports whether err is a 429 from the API.
func IsRateLimited(err error) bool { return statusOf(err) == http.StatusTooManyRequests }

func statusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Do sends any request (for endpoints newer than this SDK) and decodes the JSON response into out
// (a *[]byte receives the raw body). body is JSON-encoded unless nil.
// Generated body structs omit nil fields; to clear a field with an explicit null, pass a map here.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any, opts ...RequestOption) error {
	return c.do(ctx, method, path, query, body, out, opts)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any, opts []RequestOption) error {
	cfg := requestConfig{headers: http.Header{}}
	for _, o := range opts {
		o(&cfg)
	}
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var payload []byte
	if !isNil(body) {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	if method == http.MethodPost && cfg.idempotencyKey == "" {
		cfg.idempotencyKey = newUUID()
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header = cfg.headers.Clone()
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "pitchmentor-go/"+Version)
		// Fastify rejects an empty body with a JSON content-type, so only set it when there is a body
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.Body, req.ContentLength = nil, 0
		}
		if cfg.idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", cfg.idempotencyKey)
		}

		res, err := c.HTTPClient.Do(req)
		if err != nil {
			if ctx.Err() == nil && attempt < c.MaxRetries {
				if err := c.sleep(ctx, backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("pitchmentor: %s %s: %w", method, path, err)
		}
		data, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if (res.StatusCode == 429 || res.StatusCode >= 500) && attempt < c.MaxRetries {
			wait := backoff(attempt)
			if ra := res.Header.Get("Retry-After"); ra != "" {
				wait = min(parseRetryAfter(ra), maxRetryDelay)
			}
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}
		if readErr != nil {
			return readErr
		}
		if res.StatusCode >= 300 {
			return errorFromResponse(res, data)
		}
		if raw, ok := out.(*[]byte); ok {
			*raw = data
			return nil
		}
		if out == nil || len(data) == 0 {
			return nil
		}
		return json.Unmarshal(data, out)
	}
}

func errorFromResponse(res *http.Response, data []byte) *Error {
	e := &Error{Status: res.StatusCode, Header: res.Header, Body: data, Code: "error"}
	if res.StatusCode >= 500 {
		e.Code = "internal_error"
	}
	// v1 shape is {"error":{"code","message"}}; tolerate {"error":"message"} and plain text too
	var v1 struct {
		Error json.RawMessage `json:"error"`
	}
	var detail struct{ Code, Message string }
	var msg string
	switch {
	case json.Unmarshal(data, &v1) == nil && json.Unmarshal(v1.Error, &detail) == nil && detail.Message != "":
		e.Code, e.Message = detail.Code, detail.Message
	case v1.Error != nil && json.Unmarshal(v1.Error, &msg) == nil:
		e.Message = msg
	case len(data) > 0:
		e.Message = string(data[:min(len(data), 500)])
	default:
		e.Message = http.StatusText(res.StatusCode)
	}
	return e
}

func paginate[T any](fetch func(cursor *string) (*Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var cursor *string
		for {
			page, err := fetch(cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range page.Data {
				if !yield(item, nil) {
					return
				}
			}
			if !page.HasMore || page.NextCursor == nil {
				return
			}
			cursor = page.NextCursor
		}
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

func setQuery(q url.Values, key string, v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	q.Set(key, fmt.Sprint(rv.Elem().Interface()))
}

func parseRetryAfter(v string) time.Duration {
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(math.Max(0, secs) * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, time.Until(t))
	}
	return 0
}

// backoff: ~0.5s, 1s, 2s … capped at 8s, with jitter.
func backoff(attempt int) time.Duration {
	d := min(8*time.Second, 500*time.Millisecond<<attempt)
	return time.Duration(float64(d) * (0.75 + mrand.Float64()*0.5))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Ptr returns a pointer to v, for optional fields and params: &ListCallsParams{Limit: pitchmentor.Ptr(50)}.
func Ptr[T any](v T) *T { return &v }
