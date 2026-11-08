package relume

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maxBodyBytes: bodies above this are refused with 413. Relume's calls are a
// few hundred bytes.
const maxBodyBytes = 1 << 20

type response struct {
	status int
	header map[string]string
	// nil for no body.
	body any
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func jsonResponse(status int, body any) response {
	return response{status: status, body: body}
}

func errorResponse(status int, code, message string) response {
	return jsonResponse(status, struct {
		Error errorBody `json:"error"`
	}{errorBody{code, message}})
}

func write(w http.ResponseWriter, r *http.Request, resp response) {
	header := w.Header()
	for name, value := range resp.header {
		header.Set(name, value)
	}
	if resp.body == nil {
		w.WriteHeader(resp.status)
		return
	}
	encoded, err := json.Marshal(resp.body)
	if err != nil {
		resp = errInternal
		encoded, _ = json.Marshal(resp.body)
	}
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(resp.status)
	if r.Method != http.MethodHead {
		w.Write(encoded)
	}
}

// checkMethod answers a CORS preflight that reached the handler (204, no
// CORS grant: your CORS middleware answers preflights in front of it) and a
// wrong method (405). There is no HEAD on the session route: it would mint
// a client secret per probe.
func checkMethod(w http.ResponseWriter, r *http.Request, allowed string) bool {
	if r.Method == allowed {
		return true
	}
	if r.Method == http.MethodOptions {
		write(w, r, response{status: http.StatusNoContent, header: map[string]string{"Allow": allowed}})
		return false
	}
	resp := errorResponse(405, "method_not_allowed", "Use "+allowed+" for this route.")
	resp.header = map[string]string{"Allow": allowed}
	write(w, r, resp)
	return false
}

// SessionHandler serves GET /relume/session for @relume_io/partner-embed:
// refuses a request from another site or without the embed's Relume-Embed
// header, works out the signed-in user with UserIDFromRequest, calls
// POST /sessions and returns the client secret, or passes the API's error
// through unchanged.
func (c *Client) SessionHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if checkMethod(w, r, http.MethodGet) {
			write(w, r, c.handleSession(r))
		}
	})
}

// CallsHandler serves POST /relume/calls, Relume's signed calls. Set your
// endpoint URL in Relume's partner settings to where it is mounted. It reads
// the raw body itself: don't put a body-parsing middleware in front of it.
func (c *Client) CallsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !checkMethod(w, r, http.MethodPost) {
			return
		}
		if r.ContentLength > maxBodyBytes {
			write(w, r, errorResponse(413, "payload_too_large", "The body is too large."))
			return
		}
		rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				write(w, r, errorResponse(413, "payload_too_large", "The body is too large."))
				return
			}
			c.onHandlerError(err, HandlerErrorInfo{Route: RouteCalls})
			write(w, r, errInternal)
			return
		}
		write(w, r, c.handleCall(r.Context(), rawBody, r.Header.Get(SignatureHeader), r.Header.Get(RequestIDHeader)))
	})
}

// Handler serves both routes, by the last segment of the path: …/session and
// …/calls. Mount it under a prefix of its own, e.g.
//
//	mux.Handle("/relume/", client.Handler())
//
// since it answers any path ending in /session or /calls that reaches it.
func (c *Client) Handler() http.Handler {
	session, calls := c.SessionHandler(), c.CallsHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimRight(r.URL.Path, "/")
		switch path[strings.LastIndex(path, "/")+1:] {
		case "session":
			session.ServeHTTP(w, r)
		case "calls":
			calls.ServeHTTP(w, r)
		default:
			write(w, r, errorResponse(404, "not_found", "Not found."))
		}
	})
}
