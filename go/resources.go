package relume

// A method for every partner API endpoint (protocol section 7). Each returns
// an *Error when the API refuses.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// segment is a path segment from a caller's id: never empty, always escaped,
// never a dot segment.
func segment(name, value string) (string, error) {
	if value == "" {
		return "", errors.New("relume: " + name + " must be a non-empty string")
	}
	// URL normalization (Cloudflare's included) resolves "." and "..", even
	// escaped as %2E, so they would reach another endpoint.
	if value == "." || value == ".." {
		return "", errors.New("relume: " + name + ` can't be "` + value + `"`)
	}
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20"), nil
}

func getJSON[T any](ctx context.Context, c *Client, path string, valid func(*T) bool) (*T, error) {
	return getWithQuery(ctx, c, path, nil, valid)
}

func getWithQuery[T any](ctx context.Context, c *Client, path string, query url.Values, valid func(*T) bool) (*T, error) {
	return requestJSON(ctx, c, requestSpec{method: http.MethodGet, path: path, query: query, retry: retrySafe}, valid)
}

func pageQuery(query url.Values, limit int) url.Values {
	if limit != 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return query
}

func newList[T any](ctx context.Context, c *Client, path string, query url.Values, after string, err error) *List[T] {
	return &List[T]{
		ctx:   ctx,
		after: after,
		next:  after,
		err:   err,
		fetch: func(ctx context.Context, after string) (Page[T], error) {
			q := url.Values{}
			for key, values := range query {
				q[key] = values
			}
			if after != "" {
				q.Set("after", after)
			}
			page, err := getWithQuery(ctx, c, path, q, func(p *wirePage[T]) bool {
				return p.Data != nil && p.NextCursor != nil && p.HasMore != nil
			})
			if err != nil {
				return Page[T]{}, err
			}
			return Page[T]{Data: page.Data, NextCursor: *page.NextCursor, HasMore: *page.HasMore}, nil
		},
	}
}

// wirePage tells a missing nextCursor or hasMore from a zero one.
type wirePage[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"nextCursor"`
	HasMore    *bool   `json:"hasMore"`
}

func validUser(u *User) bool { return u.UserID != "" && u.Status != "" }

// CreateSession is POST /sessions: a single-use client secret for this
// user's embed. The first call for a userID creates their Relume account
// with user.get. The session route (SessionHandler) calls it for you.
func (c *Client) CreateSession(ctx context.Context, userID string) (*Session, error) {
	return requestJSON(ctx, c, requestSpec{
		method: http.MethodPost,
		path:   "/sessions",
		body:   map[string]string{"userId": userID},
		// Safe to retry: Relume provisions a new user once (later sessions
		// wait for or find the account), so a retry at most mints another
		// client secret.
		retry: retrySafe,
	}, func(s *Session) bool { return s.ClientSecret != "" && !s.ExpiresAt.IsZero() })
}

// GetUser is GET /users/{userId}. CodeUserNotFound until their first session.
func (c *Client) GetUser(ctx context.Context, userID string) (*User, error) {
	id, err := segment("userID", userID)
	if err != nil {
		return nil, err
	}
	return getJSON(ctx, c, "/users/"+id, validUser)
}

// DeactivateUser is POST /users/{userId}/deactivate: cuts off their sessions
// and export links; data is kept. Idempotent, and retried on a 5xx.
func (c *Client) DeactivateUser(ctx context.Context, userID string) (*User, error) {
	return c.userAction(ctx, userID, "deactivate")
}

// ReactivateUser is POST /users/{userId}/reactivate. CodeUserSuspended if
// Relume staff suspended them.
func (c *Client) ReactivateUser(ctx context.Context, userID string) (*User, error) {
	return c.userAction(ctx, userID, "reactivate")
}

func (c *Client) userAction(ctx context.Context, userID, action string) (*User, error) {
	id, err := segment("userID", userID)
	if err != nil {
		return nil, err
	}
	return requestJSON(ctx, c, requestSpec{method: http.MethodPost, path: "/users/" + id + "/" + action, retry: retrySafe}, validUser)
}

// ListSites is GET /users/{userId}/sites, most recently updated first. Fetch
// from the beginning each time: new and changed sites sort to the front, so
// a stored cursor never shows them.
func (c *Client) ListSites(ctx context.Context, userID string, query PageQuery) *List[Site] {
	id, err := segment("userID", userID)
	return newList[Site](ctx, c, "/users/"+id+"/sites", pageQuery(url.Values{}, query.Limit), query.After, err)
}

// CreateSite is POST /users/{userId}/sites: an empty site, opened with
// relume.open({ site: siteId }) in the embed. Not idempotent, so a 5xx isn't
// retried.
func (c *Client) CreateSite(ctx context.Context, userID string, params CreateSiteParams) (*CreatedSite, error) {
	id, err := segment("userID", userID)
	if err != nil {
		return nil, err
	}
	body := map[string]string{}
	if params.Name != "" {
		body["name"] = params.Name
	}
	return requestJSON(ctx, c, requestSpec{method: http.MethodPost, path: "/users/" + id + "/sites", body: body, retry: retryUnsafe},
		func(s *CreatedSite) bool { return s.SiteID != "" })
}

// GetSite is GET /sites/{siteId}.
func (c *Client) GetSite(ctx context.Context, siteID string) (*Site, error) {
	id, err := segment("siteID", siteID)
	if err != nil {
		return nil, err
	}
	return getJSON(ctx, c, "/sites/"+id, func(s *Site) bool { return s.SiteID != "" && s.UserID != "" })
}

// GetExport is GET /sites/{siteId}/exports/{exportId}. Not available yet:
// answers CodeNotImplemented (501) until exports ship.
func (c *Client) GetExport(ctx context.Context, siteID, exportID string) (*Export, error) {
	site, err := segment("siteID", siteID)
	if err != nil {
		return nil, err
	}
	export, err := segment("exportID", exportID)
	if err != nil {
		return nil, err
	}
	return getJSON(ctx, c, "/sites/"+site+"/exports/"+export, func(e *Export) bool { return e.ExportID != "" && e.Status != "" })
}

// ListLedger is GET /ledger, in change order: an entry appears again each
// time its status changes, so upsert on ID. Store the list's NextCursor when
// you're done and pass it as After to get only what changed since.
func (c *Client) ListLedger(ctx context.Context, query PageQuery) *List[LedgerEntry] {
	return newList[LedgerEntry](ctx, c, "/ledger", pageQuery(url.Values{}, query.Limit), query.After, nil)
}

// GetLedgerEntry is GET /ledger/{id}: one entry by chargeId or reversalId,
// with PartnerResponse.
func (c *Client) GetLedgerEntry(ctx context.Context, id string) (*LedgerEntryDetail, error) {
	entry, err := segment("id", id)
	if err != nil {
		return nil, err
	}
	return getJSON(ctx, c, "/ledger/"+entry, func(e *LedgerEntryDetail) bool { return e.ID != "" && e.Type != "" && e.Status != "" })
}

// Usage is GET /usage: credits totalled over [From, To), ordered by day,
// user, action.
func (c *Client) Usage(ctx context.Context, params UsageParams) *List[UsageRow] {
	query := url.Values{"from": {params.From}, "to": {params.To}}
	if len(params.GroupBy) > 0 {
		groups := make([]string, len(params.GroupBy))
		for i, group := range params.GroupBy {
			groups[i] = string(group)
		}
		query.Set("groupBy", strings.Join(groups, ","))
	}
	return newList[UsageRow](ctx, c, "/usage", pageQuery(query, params.Limit), params.After, nil)
}

// ListPrices is GET /prices: every billable action's price in this
// environment.
func (c *Client) ListPrices(ctx context.Context) ([]Price, error) {
	list, err := c.GetPrices(ctx)
	if err != nil {
		return nil, err
	}
	return list.Data, nil
}

// GetPrices is GET /prices: the prices, and what the builder calls your
// credits.
func (c *Client) GetPrices(ctx context.Context) (*PriceList, error) {
	return getJSON(ctx, c, "/prices", func(p *PriceList) bool {
		return p.Data != nil && p.CreditUnit.Singular != "" && p.CreditUnit.Plural != ""
	})
}
