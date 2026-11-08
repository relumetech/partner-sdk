package relume

// GET /relume/session mints a client secret for whoever the request's
// cookies say is signed in. Any site can make a signed-in user's browser send
// that request; only the partner's CORS stops it reading the answer, and a
// CORS setup that reflects Origin would hand the secret over. So the route
// also refuses what only another site sends.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// EmbedHeader is sent by @relume_io/partner-embed on every session
	// request. A custom header makes a cross-origin request need a CORS
	// preflight, which a plain link, form or <img> on another site can't get.
	// A partner on another origin lists it in Access-Control-Allow-Headers.
	EmbedHeader      = "Relume-Embed"
	EmbedHeaderValue = "1"
)

func (c *Client) handleSession(r *http.Request) response {
	info := HandlerErrorInfo{Route: RouteSession}

	// Browsers send Sec-Fetch-Site on every request. Another site is never a
	// legitimate caller, whatever the partner's CORS allows.
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return errorResponse(403, "cross_site_request", "The session route only answers your own pages.")
	}
	if strings.TrimSpace(r.Header.Get(EmbedHeader)) != EmbedHeaderValue {
		c.onHandlerError(fmt.Errorf("relume: GET /relume/session was refused: no %s: %s header. @relume_io/partner-embed sends it from 2.0.0; update the script tag or package. On another origin, allow the header in the route's CORS preflight", EmbedHeader, EmbedHeaderValue), info)
		return errorResponse(400, "embed_header_missing", fmt.Sprintf("The session route needs the %s header that @relume_io/partner-embed sends.", EmbedHeader))
	}

	if c.options.UserIDFromRequest == nil {
		c.onHandlerError(errors.New("relume: no UserIDFromRequest callback is set"), info)
		return errInternal
	}
	userID, err := c.userIDFromRequest(r)
	if err != nil {
		c.onHandlerError(err, info)
		return errInternal
	}
	if userID == "" {
		return errorResponse(401, "not_signed_in", "Nobody is signed in.")
	}
	if utf16Len(userID) > 128 {
		c.onHandlerError(errors.New("relume: UserIDFromRequest must return a user id of 1 to 128 characters, or \"\""), info)
		return errInternal
	}

	// One attempt, no retries: a browser is waiting on this route.
	res, err := c.do(r.Context(), http.MethodPost, "/sessions", nil, map[string]string{"userId": userID})
	if err != nil {
		// A browser that went away isn't the partner's problem.
		if r.Context().Err() == nil {
			c.onHandlerError(err, info)
		}
		status := 502
		var apiErr *Error
		if errors.As(err, &apiErr) && apiErr.Code == CodeTimeout {
			status = 504
		}
		return errorResponse(status, "session_failed", "Couldn't reach Relume.")
	}

	info.RequestID = res.requestID
	if res.status >= 200 && res.status < 300 {
		// Passed on as Relume sent them, not re-formatted.
		var session struct {
			ClientSecret string `json:"clientSecret"`
			ExpiresAt    string `json:"expiresAt"`
		}
		if json.Unmarshal(res.body, &session) == nil && session.ClientSecret != "" && session.ExpiresAt != "" {
			return jsonResponse(200, session)
		}
		c.onHandlerError(errorFromResponse(res), info)
		return errorResponse(502, "session_failed", "Relume answered without a session.")
	}

	if res.status == 401 {
		// Our own key was refused: the partner's problem, worth logging.
		c.onHandlerError(errorFromResponse(res), info)
	}
	// Pass Relume's error through, so the embed can tell user_deactivated
	// from a failure. A body without error gets a generic one.
	var body map[string]json.RawMessage
	if json.Unmarshal(res.body, &body) == nil && body["error"] != nil {
		return jsonResponse(res.status, json.RawMessage(res.body))
	}
	return errorResponse(res.status, "session_failed", fmt.Sprintf("Relume answered %d.", res.status))
}

// userIDFromRequest turns a panic into an error, as the call callbacks do.
func (c *Client) userIDFromRequest(r *http.Request) (userID string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("relume: UserIDFromRequest panicked: %v", recovered)
		}
	}()
	return c.options.UserIDFromRequest(r)
}
