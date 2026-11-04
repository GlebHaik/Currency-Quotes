// Package frankfurter is a client for the Frankfurter exchange-rate API (https://frankfurter.dev).
package frankfurter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

// maxBodySize caps how much of a response is read, so a broken provider can't exhaust memory.
const maxBodySize = 1 << 20

type Config struct {
	BaseURL string
	Timeout time.Duration
	// RPS and Burst cap the requests this client sends; RPS <= 0 means no limit.
	RPS   float64
	Burst int
}

// Client calls GET /v2/rate/{base}/{quote}. Share one Client between all workers:
// its limiter then caps the total RPS the API sees.
type Client struct {
	baseURL string
	http    *http.Client
	limiter *rate.Limiter
}

func New(cfg Config) *Client {
	limit := rate.Inf
	if cfg.RPS > 0 {
		limit = rate.Limit(cfg.RPS)
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		http:    &http.Client{Timeout: cfg.Timeout},
		limiter: rate.NewLimiter(limit, max(cfg.Burst, 1)),
	}
}

func (c *Client) Rate(ctx context.Context, pair domain.CurrencyPair) (domain.Rate, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return domain.Rate{}, fmt.Errorf("frankfurter: rate limit: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v2/rate/%s/%s", c.baseURL,
		url.PathEscape(strings.ToLower(pair.BaseCurrency())), url.PathEscape(strings.ToLower(pair.QuoteCurrency())))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return domain.Rate{}, fmt.Errorf("frankfurter: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.Rate{}, fmt.Errorf("frankfurter: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return domain.Rate{}, fmt.Errorf("frankfurter: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return domain.Rate{}, newError(resp, body)
	}

	var payload struct {
		Date string      `json:"date"`
		Rate json.Number `json:"rate"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	// UseNumber keeps "20.955" as text
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil || payload.Rate == "" {
		return domain.Rate{}, fmt.Errorf("frankfurter: unexpected body %q", body)
	}
	date, err := time.Parse(time.DateOnly, payload.Date)
	if err != nil {
		return domain.Rate{}, fmt.Errorf("frankfurter: unexpected date %q", payload.Date)
	}
	return domain.Rate{Price: payload.Rate.String(), Date: date}, nil
}

type Error struct {
	StatusCode int
	Message    string
	Permanent  bool
	retryAfter time.Duration
}

func (e *Error) Error() string {
	return fmt.Sprintf("frankfurter: %s (HTTP %d)", e.Message, e.StatusCode)
}

func (e *Error) Is(target error) bool {
	return target == domain.ErrPermanent && e.Permanent
}

func (e *Error) RetryAfter() time.Duration {
	return e.retryAfter
}

func newError(resp *http.Response, body []byte) *Error {
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Message == "" {
		payload.Message = http.StatusText(resp.StatusCode)
	}

	e := &Error{StatusCode: resp.StatusCode, Message: payload.Message}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			e.retryAfter = time.Duration(secs) * time.Second
		}
	case resp.StatusCode >= 500:
	default:
		// Other 4xx, such as 422 for an unknown currency: the same request would fail again.
		e.Permanent = true
	}
	return e
}
