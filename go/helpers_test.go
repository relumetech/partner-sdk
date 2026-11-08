package relume

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSecret = "sk_test_helpers_key_0000000000000000"

var testNow = time.Unix(1793059200, 0)

// newTestClient has a fixed clock, records sleeps instead of waiting and
// collects handler errors.
type testClient struct {
	*Client
	mu     sync.Mutex
	errors []error
	infos  []HandlerErrorInfo
	sleeps []time.Duration
}

func newTestClient(t *testing.T, options Options) *testClient {
	t.Helper()
	if options.SecretKey == "" {
		options.SecretKey = testSecret
	}
	tc := &testClient{}
	if options.OnHandlerError == nil {
		options.OnHandlerError = func(err error, info HandlerErrorInfo) {
			tc.mu.Lock()
			defer tc.mu.Unlock()
			tc.errors = append(tc.errors, err)
			tc.infos = append(tc.infos, info)
		}
	}
	client, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.now = func() time.Time { return testNow }
	client.sleep = func(ctx context.Context, d time.Duration) error {
		tc.mu.Lock()
		defer tc.mu.Unlock()
		tc.sleeps = append(tc.sleeps, d)
		return ctx.Err()
	}
	tc.Client = client
	return tc
}

// allCallbacks answers every call successfully.
func allCallbacks(options Options) Options {
	if options.OnGetUser == nil {
		options.OnGetUser = func(context.Context, string, CallContext) (*UserProfile, error) {
			return &UserProfile{Name: "Zoë"}, nil
		}
	}
	if options.OnGetCredits == nil {
		options.OnGetCredits = func(context.Context, string, CallContext) (Credits, error) {
			return Credits{Balance: 1}, nil
		}
	}
	if options.OnChargeCredits == nil {
		options.OnChargeCredits = func(context.Context, Charge, CallContext) (ChargeResult, error) {
			return ChargeResult{Status: Charged, Balance: intPtr(1)}, nil
		}
	}
	if options.OnReverseCharge == nil {
		options.OnReverseCharge = func(context.Context, Reversal, CallContext) (ReversalResult, error) {
			return ReversalResult{Balance: intPtr(1)}, nil
		}
	}
	return options
}

func intPtr(n int) *int { return &n }

// postCall sends a call signed with secret at timestamp.
func postCall(t *testing.T, handler http.Handler, secret string, timestamp time.Time, body string) *httptest.ResponseRecorder {
	t.Helper()
	header, err := SignCall([]string{secret}, timestamp.Unix(), []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/relume/calls", strings.NewReader(body))
	req.Header.Set(SignatureHeader, header)
	req.Header.Set(RequestIDHeader, "req_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func (tc *testClient) call(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postCall(t, tc.CallsHandler(), testSecret, testNow, body)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// decode reads a recorded JSON body.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q isn't JSON: %v", rec.Body.String(), err)
	}
	return body
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	errorObject, _ := decode(t, rec)["error"].(map[string]any)
	code, _ := errorObject["code"].(string)
	return code
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
	if code != "" {
		if got := errorCode(t, rec); got != code {
			t.Fatalf("error code %q, want %q; body %s", got, code, rec.Body.String())
		}
	}
}

// apiRequest is one request the fake partner API received.
type apiRequest struct {
	method string
	path   string
	query  string
	header http.Header
	body   string
}

// fakeAPI answers each request with the next reply, recording what it got.
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests []apiRequest
	replies  []func(w http.ResponseWriter)
}

func newFakeAPI(t *testing.T, replies ...func(w http.ResponseWriter)) *fakeAPI {
	t.Helper()
	api := &fakeAPI{replies: replies}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.requests = append(api.requests, apiRequest{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), string(body)})
		var reply func(w http.ResponseWriter)
		if len(api.replies) > 0 {
			reply, api.replies = api.replies[0], api.replies[1:]
		}
		api.mu.Unlock()
		if reply == nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(599)
			return
		}
		reply(w)
	}))
	t.Cleanup(api.Close)
	return api
}

func reply(status int, body string, headers ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set(RequestIDHeader, "req_"+strconv.Itoa(status))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func apiError(status int, code string) func(w http.ResponseWriter) {
	return reply(status, `{"error":{"code":"`+code+`","message":"Refused: `+code+`."}}`)
}

func (api *fakeAPI) got() []apiRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]apiRequest(nil), api.requests...)
}

// newAPIClient points a test client at the fake API.
func newAPIClient(t *testing.T, api *fakeAPI, options Options) *testClient {
	t.Helper()
	options.APIBaseURL = api.URL
	return newTestClient(t, options)
}
