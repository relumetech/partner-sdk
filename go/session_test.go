package relume

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sessionReply = `{"clientSecret":"cs_test_1","expiresAt":"2026-10-27T00:05:00.000Z"}`

func signedIn(userID string) func(*http.Request) (string, error) {
	return func(*http.Request) (string, error) { return userID, nil }
}

// getSession is the embed's GET /relume/session from the partner's own page.
func getSession(handler http.Handler, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/relume/session", nil)
	req.Header.Set(EmbedHeader, EmbedHeaderValue)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestSessionRoute(t *testing.T) {
	t.Run("creates a session for the signed-in user with the secret key", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, sessionReply))
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		rec := getSession(client.SessionHandler())
		// expiresAt is passed on as Relume sent it, milliseconds included.
		if rec.Code != 200 || rec.Body.String() != sessionReply || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), rec.Header())
		}
		got := api.got()
		if len(got) != 1 || got[0].method != "POST" || got[0].path != "/sessions" || got[0].body != `{"userId":"42"}` ||
			got[0].header.Get("Authorization") != "Bearer "+testSecret ||
			got[0].header.Get("User-Agent") != "relume-partner-go/"+Version {
			t.Errorf("requests %+v", got)
		}
	})
	t.Run("answers 401 not_signed_in without calling Relume", func(t *testing.T) {
		api := newFakeAPI(t)
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("")})
		expectStatus(t, getSession(client.SessionHandler()), 401, "not_signed_in")
		if len(api.got()) != 0 {
			t.Error("called Relume")
		}
	})
	t.Run("passes Relume's error through with the same status", func(t *testing.T) {
		api := newFakeAPI(t, apiError(403, "user_deactivated"))
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		rec := getSession(client.SessionHandler())
		expectStatus(t, rec, 403, "user_deactivated")
		if len(client.errors) != 0 {
			t.Errorf("logged %v", client.errors)
		}
	})
	t.Run("gives an error without a body a generic one", func(t *testing.T) {
		api := newFakeAPI(t, reply(503, "<html>down</html>"))
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		expectStatus(t, getSession(client.SessionHandler()), 503, "session_failed")
	})
	t.Run("answers 500 when UserIDFromRequest fails or returns junk", func(t *testing.T) {
		for _, resolve := range []func(*http.Request) (string, error){
			func(*http.Request) (string, error) { return "", errors.New("session store down") },
			func(*http.Request) (string, error) { panic("nil session") },
			signedIn(strings.Repeat("x", 129)),
		} {
			api := newFakeAPI(t)
			client := newAPIClient(t, api, Options{UserIDFromRequest: resolve})
			expectStatus(t, getSession(client.SessionHandler()), 500, "internal_error")
			if len(client.errors) != 1 || client.infos[0].Route != RouteSession {
				t.Errorf("handler errors %v %+v", client.errors, client.infos)
			}
		}
	})
	t.Run("answers 500 when UserIDFromRequest is not set", func(t *testing.T) {
		client := newTestClient(t, Options{})
		expectStatus(t, getSession(client.SessionHandler()), 500, "internal_error")
		if len(client.errors) != 1 {
			t.Errorf("handler errors %v", client.errors)
		}
	})
	t.Run("answers 502 session_failed when Relume is unreachable", func(t *testing.T) {
		api := newFakeAPI(t)
		api.Close()
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		expectStatus(t, getSession(client.SessionHandler()), 502, "session_failed")
	})
	t.Run("answers 504 session_failed when Relume times out", func(t *testing.T) {
		api := newFakeAPI(t, func(w http.ResponseWriter) { time.Sleep(200 * time.Millisecond) })
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42"), APITimeout: 20 * time.Millisecond})
		expectStatus(t, getSession(client.SessionHandler()), 504, "session_failed")
	})
	t.Run("doesn't report a browser that went away", func(t *testing.T) {
		api := newFakeAPI(t, func(w http.ResponseWriter) { time.Sleep(200 * time.Millisecond) })
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/relume/session", nil)
		req.Header.Set(EmbedHeader, EmbedHeaderValue)
		client.SessionHandler().ServeHTTP(httptest.NewRecorder(), req)
		if len(client.errors) != 0 {
			t.Errorf("handler errors %v", client.errors)
		}
	})
	t.Run("answers 502 when Relume answers 2xx without a session", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, `{"clientSecret":"cs_1"}`))
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		expectStatus(t, getSession(client.SessionHandler()), 502, "session_failed")
	})
	t.Run("logs and passes through a 401 for its own secret key", func(t *testing.T) {
		api := newFakeAPI(t, apiError(401, "invalid_api_key"))
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		expectStatus(t, getSession(client.SessionHandler()), 401, "invalid_api_key")
		var apiErr *Error
		if len(client.errors) != 1 || !errors.As(client.errors[0], &apiErr) || apiErr.Code != "invalid_api_key" || client.infos[0].RequestID != "req_401" {
			t.Errorf("handler errors %v %+v", client.errors, client.infos)
		}
	})
}

func TestSessionRouteAgainstOtherSites(t *testing.T) {
	t.Run("refuses a request from another site, even with the header", func(t *testing.T) {
		api := newFakeAPI(t)
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		expectStatus(t, getSession(client.SessionHandler(), "Sec-Fetch-Site", " Cross-Site "), 403, "cross_site_request")
		if len(api.got()) != 0 {
			t.Error("called Relume")
		}
	})
	t.Run("refuses a request without the header, and says why", func(t *testing.T) {
		for _, value := range []string{"", "0", "true"} {
			api := newFakeAPI(t)
			client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
			expectStatus(t, getSession(client.SessionHandler(), EmbedHeader, value), 400, "embed_header_missing")
			if len(client.errors) != 1 || !strings.Contains(client.errors[0].Error(), EmbedHeader) || len(api.got()) != 0 {
				t.Errorf("%q: handler errors %v", value, client.errors)
			}
		}
	})
	t.Run("answers the embed from the same origin, the same site or a browser without fetch metadata", func(t *testing.T) {
		for _, site := range []string{"same-origin", "same-site", "none", ""} {
			api := newFakeAPI(t, reply(200, sessionReply))
			client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
			expectStatus(t, getSession(client.SessionHandler(), "Sec-Fetch-Site", site), 200, "")
		}
	})
}

func TestCreateSession(t *testing.T) {
	api := newFakeAPI(t, reply(200, sessionReply), apiError(403, "user_suspended"))
	client := newAPIClient(t, api, Options{})
	session, err := client.CreateSession(context.Background(), "42")
	if err != nil || session.ClientSecret != "cs_test_1" || !session.ExpiresAt.Equal(time.Date(2026, 10, 27, 0, 5, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", session, err)
	}
	_, err = client.CreateSession(context.Background(), "42")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || apiErr.Code != CodeUserSuspended {
		t.Errorf("got %v", err)
	}
}
