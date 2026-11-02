package provider

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
)

// Frankfurter calls https://api.frankfurter.dev: GET /v2/rate/{base}/{quote}.
type Frankfurter struct {
	baseURL string
	client  *http.Client
}

func NewFrankfurter(baseURL string, timeout time.Duration) *Frankfurter {
	return &Frankfurter{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

func (p *Frankfurter) Rate(ctx context.Context, base, quote string) (Rate, error) {
	endpoint := fmt.Sprintf("%s/v2/rate/%s/%s", p.baseURL,
		url.PathEscape(strings.ToLower(base)), url.PathEscape(strings.ToLower(quote)))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Rate{}, fmt.Errorf("frankfurter: build request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return Rate{}, fmt.Errorf("frankfurter: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Rate{}, fmt.Errorf("frankfurter: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Rate{}, newError(resp, body)
	}

	var payload struct {
		Date string      `json:"date"`
		Rate json.Number `json:"rate"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	// UseNumber keeps "20.955" as text
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil || payload.Rate == "" {
		return Rate{}, fmt.Errorf("frankfurter: unexpected body %q", body)
	}
	date, err := time.Parse(time.DateOnly, payload.Date)
	if err != nil {
		return Rate{}, fmt.Errorf("frankfurter: unexpected date %q", payload.Date)
	}
	return Rate{Price: payload.Rate.String(), Date: date}, nil
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
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	case resp.StatusCode >= 500:
		// Retry
	default:
		// Other 4xx
		e.Permanent = true
	}
	return e
}
