package relume

// The partner API (Relume Partner Protocol v1, section 7), authenticated
// with the secret key: one request, the retry loop around it, and the errors
// it returns. The typed methods are in resources.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Codes the SDK sets itself, when no usable answer arrived. Every other code
// is the API's error.code (protocol section 9); new ones may appear.
const (
	CodeTimeout            = "timeout"
	CodeNetworkError       = "network_error"
	CodeUnexpectedResponse = "unexpected_response"
)

// Error is an error answer from the partner API, or a request that never got
// one. Get it with errors.As and switch on Code.
type Error struct {
	// The HTTP status, or 0 when no response arrived.
	Status int
	// The API's error.code (e.g. "user_deactivated"), or CodeTimeout,
	// CodeNetworkError or CodeUnexpectedResponse.
	Code      string
	Message   string
	RequestID string

	cause    error
	redirect bool
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("relume: %s (%s)", e.Message, e.Code)
	}
	return fmt.Sprintf("relume: %s (%d %s)", e.Message, e.Status, e.Code)
}

func (e *Error) Unwrap() error { return e.cause }

const (
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 8 * time.Second
	// A 429's Retry-After longer than this isn't waited out: the 429 is
	// returned. A 5xx's Retry-After (a proxy or maintenance page) is capped at
	// the backoff ceiling instead, so the retries still happen.
	maxRetryAfter = 60 * time.Second
)

type apiResponse struct {
	status    int
	body      []byte
	requestID string
	// retryAfter is negative when absent or unreadable.
	retryAfter time.Duration
}

// do sends one request. It returns an *Error (status 0) only when no
// response arrived, or the caller's context error once ctx is done.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*apiResponse, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}
	target := c.baseURL + path
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}

	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, method, target, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Accept", "application/json")
	// Relume's firewall refuses requests with no User-Agent.
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &Error{Code: CodeTimeout, Message: fmt.Sprintf("The Relume API didn't answer within %s.", c.timeout), cause: err}
		}
		return nil, &Error{Code: CodeNetworkError, Message: "Couldn't reach the Relume API.", cause: err}
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, &Error{Code: CodeNetworkError, Message: fmt.Sprintf("The Relume API answered with a redirect (%d); check APIBaseURL.", res.StatusCode), redirect: true}
	}

	text, _ := io.ReadAll(res.Body)
	return &apiResponse{
		status:     res.StatusCode,
		body:       text,
		requestID:  res.Header.Get(RequestIDHeader),
		retryAfter: parseRetryAfter(res.Header.Get("Retry-After"), c.now()),
	}, nil
}

// parseRetryAfter reads seconds or an HTTP date.
func parseRetryAfter(header string, now time.Time) time.Duration {
	value := strings.TrimSpace(header)
	if value == "" {
		return -1
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return -1
	}
	return max(0, date.Sub(now))
}

// errorFromResponse is the API's { error: { code, message } }, or a generic one.
func errorFromResponse(res *apiResponse) *Error {
	var parsed struct {
		Error struct {
			Code    *string `json:"code"`
			Message *string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(res.body, &parsed)
	err := &Error{
		Status:    res.status,
		Code:      CodeUnexpectedResponse,
		Message:   fmt.Sprintf("The Relume API answered %d.", res.status),
		RequestID: res.requestID,
	}
	if parsed.Error.Code != nil {
		err.Code = *parsed.Error.Code
	}
	if parsed.Error.Message != nil {
		err.Message = *parsed.Error.Message
	}
	return err
}

// retryPolicy: every request retries a 429, which is refused before any
// work. Unsafe requests (not idempotent, with a lasting effect, like
// creating a site) retry nothing else: a 5xx or a timeout may mean the work
// was done.
type retryPolicy int

const (
	retrySafe retryPolicy = iota
	retryUnsafe
)

type requestSpec struct {
	method string
	path   string
	query  url.Values
	body   any
	retry  retryPolicy
}

// requestJSON sends a request with retries (429s after Retry-After; for safe
// requests 5xx answers and failed connections with exponential backoff and
// jitter) and decodes a 2xx into T. valid is a light check that the body is
// the object asked for.
func requestJSON[T any](ctx context.Context, c *Client, spec requestSpec, valid func(*T) bool) (*T, error) {
	for attempt := 0; ; attempt++ {
		canRetry := attempt < c.maxRetries
		res, err := c.do(ctx, spec.method, spec.path, spec.query, spec.body)
		if err != nil {
			var apiErr *Error
			// Not transient: the caller gave up, or a redirect, which a retry
			// only delays.
			transient := errors.As(err, &apiErr) && !apiErr.redirect
			if !canRetry || spec.retry != retrySafe || !transient {
				return nil, err
			}
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}

		if res.status >= 200 && res.status < 300 {
			var result T
			if json.Unmarshal(res.body, &result) == nil && valid(&result) {
				return &result, nil
			}
			return nil, &Error{
				Status:    res.status,
				Code:      CodeUnexpectedResponse,
				Message:   fmt.Sprintf("%s %s answered %d without the expected body.", spec.method, spec.path, res.status),
				RequestID: res.requestID,
			}
		}

		// Every other 5xx is retried, Cloudflare's 520 to 527 included: 501
		// and 505 won't change on a retry.
		retryable := res.status == 429 ||
			(spec.retry == retrySafe && res.status >= 500 && res.status != 501 && res.status != 505)
		delay := backoff(attempt)
		if res.retryAfter >= 0 {
			delay = res.retryAfter
			if res.status != 429 {
				delay = min(delay, retryMaxDelay)
			}
		}
		if !canRetry || !retryable || delay > maxRetryAfter {
			return nil, errorFromResponse(res)
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// backoff is full jitter: anywhere up to 500ms, 1s, 2s, … capped at 8s.
func backoff(attempt int) time.Duration {
	ceiling := retryMaxDelay
	if attempt < 5 {
		ceiling = min(retryMaxDelay, retryBaseDelay<<attempt)
	}
	return time.Duration(rand.Int64N(int64(ceiling) + 1))
}
