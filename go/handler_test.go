package relume

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(handler http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set(EmbedHeader, EmbedHeaderValue)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandler(t *testing.T) {
	t.Run("routes by the last path segment wherever it is mounted", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{UserIDFromRequest: signedIn("")}))
		mux := http.NewServeMux()
		mux.Handle("/api/partners/relume/", client.Handler())
		for path, status := range map[string]int{
			"/api/partners/relume/session":  401, // not_signed_in: the session route ran
			"/api/partners/relume/session/": 401,
			"/api/partners/relume/calls":    405, // GET on the calls route
			"/api/partners/relume/other":    404,
		} {
			if rec := serve(mux, http.MethodGet, path, nil); rec.Code != status {
				t.Errorf("%s: %d, want %d; %s", path, rec.Code, status, rec.Body.String())
			}
		}
		rec := postCall(t, http.StripPrefix("/relume", client.Handler()), testSecret, testNow, `{"type":"credits.get","userId":"42"}`)
		if rec.Code != 200 {
			t.Errorf("calls through StripPrefix: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("answers 405 with Allow for the wrong method", func(t *testing.T) {
		client := newTestClient(t, Options{})
		for _, c := range []struct {
			handler       http.Handler
			method, allow string
		}{
			{client.SessionHandler(), http.MethodPost, "GET"},
			{client.CallsHandler(), http.MethodGet, "POST"},
			{client.CallsHandler(), http.MethodPut, "POST"},
		} {
			rec := serve(c.handler, c.method, "/", nil)
			expectStatus(t, rec, 405, "method_not_allowed")
			if rec.Header().Get("Allow") != c.allow {
				t.Errorf("%s: Allow %q", c.method, rec.Header().Get("Allow"))
			}
		}
	})
	t.Run("refuses HEAD on the session route without creating a session", func(t *testing.T) {
		api := newFakeAPI(t)
		client := newAPIClient(t, api, Options{UserIDFromRequest: signedIn("42")})
		rec := serve(client.SessionHandler(), http.MethodHead, "/relume/session", nil)
		if rec.Code != 405 || rec.Body.Len() != 0 || len(api.got()) != 0 {
			t.Errorf("%d %q after %d requests", rec.Code, rec.Body.String(), len(api.got()))
		}
	})
	t.Run("answers a preflight that reaches it with 204 and no CORS grant", func(t *testing.T) {
		client := newTestClient(t, Options{})
		rec := serve(client.Handler(), http.MethodOptions, "/relume/session", nil)
		if rec.Code != 204 || rec.Header().Get("Allow") != "GET" || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Body.Len() != 0 {
			t.Errorf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
		}
	})
	t.Run("answers 413 for a body over 1 MB, with or without a content-length", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{}))
		big := strings.Repeat("x", maxBodyBytes+1)
		expectStatus(t, serve(client.CallsHandler(), http.MethodPost, "/", strings.NewReader(big)), 413, "payload_too_large")
		// A reader that isn't a *strings.Reader has no content-length: chunked.
		expectStatus(t, serve(client.CallsHandler(), http.MethodPost, "/", io.MultiReader(strings.NewReader(big))), 413, "payload_too_large")
	})
	t.Run("answers a real server end to end", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{}))
		server := httptest.NewServer(client.Handler())
		defer server.Close()
		body := `{"type":"credits.get","environment":"test","userId":"42"}`
		header, _ := SignCall([]string{testSecret}, testNow.Unix(), []byte(body))
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/relume/calls", strings.NewReader(body))
		req.Header.Set(SignatureHeader, header)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		got, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || string(got) != `{"balance":1}` {
			t.Errorf("%d %s", res.StatusCode, got)
		}
	})
}
