// Package relume is the server SDK for Relume partners. It answers Relume's
// signed calls, serves builder sessions and calls the partner API, so your
// backend never writes any cryptography.
//
// It implements the server side of the Relume Partner Protocol v1 and is the
// Go counterpart of @relume_io/partner-node. Pair it with
// @relume_io/partner-embed on your page.
package relume

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Version is sent in the User-Agent of every partner API request.
const Version = "0.1.0"

const (
	DefaultAPIBaseURL = "https://apis.relume.io/nova/partner/v1"
	// DefaultAPITimeout is per attempt. POST /sessions waits on user.get for
	// a new user (4s), so it allows more.
	DefaultAPITimeout = 15 * time.Second
	// DefaultMaxRetries is the retries after the first attempt.
	DefaultMaxRetries = 2

	userAgent = "relume-partner-go/" + Version
)

// Environment is a key's environment, and the environment every signed call
// carries.
type Environment string

const (
	EnvironmentTest Environment = "test"
	EnvironmentLive Environment = "live"
)

// Route is which of the two routes a handler error came from.
type Route string

const (
	RouteSession Route = "session"
	RouteCalls   Route = "calls"
)

// HandlerErrorInfo says where OnHandlerError's error came from.
type HandlerErrorInfo struct {
	Route Route
	// Set on RouteCalls once the call's type is known.
	Type CallType
	// Relume-Request-Id, when there is one.
	RequestID string
}

type Options struct {
	// SecretKey is sk_test_… or sk_live_…. Server only: never send it to a
	// browser. Surrounding whitespace is trimmed.
	SecretKey string
	// APIBaseURL defaults to production; Relume gives you another URL for
	// staging. Must be https, since every request carries the secret key;
	// http is allowed only for localhost.
	APIBaseURL string
	// APITimeout is per partner API request attempt. Default 15 seconds.
	APITimeout time.Duration
	// MaxRetries is the retries after the first attempt. Default 2. A 429 is
	// retried after its Retry-After; a 5xx or a failed connection with
	// backoff, except on CreateSite, which isn't idempotent.
	MaxRetries int
	// DisableRetries makes every request a single attempt.
	DisableRetries bool
	// HTTPClient sends partner API requests, e.g. through a proxy. Default
	// http.DefaultClient. Redirects are never followed.
	HTTPClient *http.Client
	// OnHandlerError is called when a callback fails, panics or returns
	// something the protocol doesn't allow, or the session route fails.
	// Defaults to slog.Error. The caller always gets a 5xx; Relume never sees
	// the error's message.
	OnHandlerError func(err error, info HandlerErrorInfo)

	// UserIDFromRequest says who is signed in, for the browser's
	// GET /relume/session: your own id for the user, 1 to 128 characters,
	// or "" when nobody is. Read it from your own session; never from the
	// query string or body.
	UserIDFromRequest func(r *http.Request) (string, error)
	// OnGetUser answers user.get: the user's profile, or nil if you don't
	// know them (Relume then refuses the session).
	OnGetUser func(ctx context.Context, userID string, call CallContext) (*UserProfile, error)
	// OnGetCredits answers credits.get with the balance in your own credits.
	OnGetCredits func(ctx context.Context, userID string, call CallContext) (Credits, error)
	// OnChargeCredits answers credits.charge. Debit PartnerCredits (never
	// RelumeCredits), at most once per ChargeID. Returning an error makes the
	// outcome unknown: Relume then never does the work and always sends
	// credits.reverse for that ChargeID.
	OnChargeCredits func(ctx context.Context, charge Charge, call CallContext) (ChargeResult, error)
	// OnReverseCharge answers credits.reverse. Credit back PartnerCredits
	// (never RelumeCredits), at most once per ReversalID. If ChargeID names a
	// debit you never made, change nothing and return normally.
	OnReverseCharge func(ctx context.Context, reversal Reversal, call CallContext) (ReversalResult, error)
}

// Client is a Relume partner client. Create one with New; there is no global
// state, so test and live clients can sit side by side. Safe for concurrent
// use.
type Client struct {
	options     Options
	secretKey   string
	environment Environment
	baseURL     string
	timeout     time.Duration
	maxRetries  int
	httpClient  *http.Client

	// Overridden in tests.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func New(options Options) (*Client, error) {
	// A key read from a file or .env often ends in a newline, which would
	// make every signature check and API request fail.
	secretKey := strings.TrimSpace(options.SecretKey)
	if secretKey == "" {
		return nil, errors.New("relume.New: SecretKey is required (sk_test_… or sk_live_…). Is the environment variable set?")
	}
	if strings.HasPrefix(secretKey, "pk_") {
		return nil, errors.New("relume.New: that is a publishable key. Pass the secret key (sk_…); the publishable key goes to the embed")
	}
	environment, ok := keyEnvironment(secretKey)
	if !ok {
		return nil, errors.New("relume.New: SecretKey must start with sk_test_ or sk_live_")
	}

	baseURL := options.APIBaseURL
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	baseURL, err := checkAPIBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	maxRetries := options.MaxRetries
	switch {
	case maxRetries < 0:
		return nil, errors.New("relume.New: MaxRetries must be 0 (the default) or more; set DisableRetries to turn retries off")
	case options.DisableRetries && maxRetries > 0:
		return nil, errors.New("relume.New: set MaxRetries or DisableRetries, not both")
	case options.DisableRetries:
		maxRetries = 0
	case maxRetries == 0:
		maxRetries = DefaultMaxRetries
	}

	timeout := options.APITimeout
	switch {
	case timeout < 0:
		return nil, errors.New("relume.New: APITimeout must be 0 (the default) or more")
	case timeout == 0:
		timeout = DefaultAPITimeout
	}

	base := options.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	httpClient := *base
	// The API never redirects, so one means the base URL is wrong.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	if options.OnHandlerError == nil {
		options.OnHandlerError = defaultOnHandlerError
	}

	return &Client{
		options:     options,
		secretKey:   secretKey,
		environment: environment,
		baseURL:     baseURL,
		timeout:     timeout,
		maxRetries:  maxRetries,
		httpClient:  &httpClient,
		now:         time.Now,
		sleep:       sleepContext,
	}, nil
}

// Environment is the secret key's environment.
func (c *Client) Environment() Environment { return c.environment }

func (c *Client) onHandlerError(err error, info HandlerErrorInfo) {
	c.options.OnHandlerError(err, info)
}

func defaultOnHandlerError(err error, info HandlerErrorInfo) {
	where := "GET /relume/session"
	if info.Route == RouteCalls {
		where = "call"
		if info.Type != "" {
			where = string(info.Type)
		}
	}
	slog.Error("relume: "+where+" failed", "error", err, "requestId", info.RequestID)
}

var loopback = regexp.MustCompile(`^(localhost|127(\.[0-9]{1,3}){3}|::1)$`)

// checkAPIBaseURL refuses a URL that would send the key in clear, and drops
// trailing slashes.
func checkAPIBaseURL(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("relume.New: APIBaseURL isn't a URL: %s", value)
	}
	local := u.Scheme == "http" && loopback.MatchString(u.Hostname())
	if u.Scheme != "https" && !local {
		return "", errors.New("relume.New: APIBaseURL must be an https URL (http only for localhost): every request carries your secret key")
	}
	return strings.TrimRight(value, "/"), nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
