package relume

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	t.Run("refuses a missing secret key or a publishable key", func(t *testing.T) {
		for _, key := range []string{"", "  \n", "pk_test_abc", "pk_live_abc"} {
			if _, err := New(Options{SecretKey: key}); err == nil {
				t.Errorf("%q: no error", key)
			}
		}
	})
	t.Run("refuses a secret key that names no environment", func(t *testing.T) {
		if _, err := New(Options{SecretKey: "sk_prod_abc"}); err == nil {
			t.Error("no error")
		}
	})
	t.Run("trims whitespace from the secret key, e.g. a newline from a .env file", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{SecretKey: testSecret + "\n"}))
		expectStatus(t, client.call(t, `{"type":"credits.get","environment":"test","userId":"42"}`), 200, "")
	})
	t.Run("reads the environment from the key", func(t *testing.T) {
		live, _ := New(Options{SecretKey: "sk_live_abc"})
		test, _ := New(Options{SecretKey: "sk_test_abc"})
		if live.Environment() != EnvironmentLive || test.Environment() != EnvironmentTest {
			t.Errorf("got %s and %s", live.Environment(), test.Environment())
		}
	})
	t.Run("refuses an APIBaseURL that would send the secret key in clear", func(t *testing.T) {
		for _, base := range []string{"http://apis.relume.io/nova/partner/v1", "ftp://example.com", "not a url", "http://localhost.evil.com"} {
			if _, err := New(Options{SecretKey: testSecret, APIBaseURL: base}); err == nil {
				t.Errorf("%s: no error", base)
			}
		}
		for _, base := range []string{"https://staging-apis.relume.io/nova/partner/v1/", "http://localhost:4000", "http://127.0.0.1:4000", "http://[::1]:4000"} {
			if _, err := New(Options{SecretKey: testSecret, APIBaseURL: base}); err != nil {
				t.Errorf("%s: %v", base, err)
			}
		}
	})
	t.Run("refuses a negative MaxRetries or APITimeout, and MaxRetries with DisableRetries", func(t *testing.T) {
		for _, options := range []Options{
			{MaxRetries: -1},
			{MaxRetries: 1, DisableRetries: true},
			{APITimeout: -time.Second},
		} {
			options.SecretKey = testSecret
			if _, err := New(options); err == nil {
				t.Errorf("%+v: no error", options)
			}
		}
	})
}

func TestSignatureChecks(t *testing.T) {
	client := newTestClient(t, allCallbacks(Options{}))
	body := `{"type":"credits.get","environment":"test","userId":"42"}`

	t.Run("answers 401 invalid_signature for a bad signature", func(t *testing.T) {
		expectStatus(t, postCall(t, client.CallsHandler(), "sk_test_someone_else", testNow, body), 401, "invalid_signature")
	})
	t.Run("answers 401 for a missing header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		client.CallsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/relume/calls", strings.NewReader(body)))
		expectStatus(t, rec, 401, "invalid_signature")
	})
	t.Run("answers 401 for a timestamp more than 5 minutes old or ahead", func(t *testing.T) {
		for _, skew := range []time.Duration{-301 * time.Second, 301 * time.Second} {
			expectStatus(t, postCall(t, client.CallsHandler(), testSecret, testNow.Add(skew), body), 401, "invalid_signature")
		}
	})
	t.Run("accepts a timestamp exactly 5 minutes off", func(t *testing.T) {
		for _, skew := range []time.Duration{-300 * time.Second, 300 * time.Second} {
			expectStatus(t, postCall(t, client.CallsHandler(), testSecret, testNow.Add(skew), body), 200, "")
		}
	})
}

func TestRoutingByType(t *testing.T) {
	t.Run("answers 400 unsupported_call_type for an unknown type", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{}))
		expectStatus(t, client.call(t, `{"type":"wallet.drain","userId":"42"}`), 400, "unsupported_call_type")
	})
	t.Run("answers 400 unsupported_call_type when no callback is set, and says why", func(t *testing.T) {
		client := newTestClient(t, Options{})
		expectStatus(t, client.call(t, `{"type":"credits.get","userId":"42"}`), 400, "unsupported_call_type")
		if len(client.errors) != 1 || !strings.Contains(client.errors[0].Error(), "OnGetCredits") {
			t.Errorf("handler errors %v", client.errors)
		}
	})
	t.Run("answers 400 invalid_request for a signed body that is not JSON or not an object", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{}))
		for _, body := range []string{"not json", "[]", `"x"`, `{"type":"credits.get","userId":"42"} {}`} {
			expectStatus(t, client.call(t, body), 400, "invalid_request")
		}
	})
	t.Run("answers 400 invalid_request for invalid fields", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{}))
		charge := `"type":"credits.charge","userId":"42","chargeId":"ch_1","partnerCredits":5,"relumeCredits":1,"action":"page.unlock","description":"d"`
		for _, body := range []string{
			`{"type":"user.get"}`,
			`{"type":"user.get","userId":""}`,
			`{"type":"user.get","userId":42}`,
			`{"type":"user.get","userId":"` + strings.Repeat("x", 129) + `"}`,
			`{` + strings.Replace(charge, `"partnerCredits":5`, `"partnerCredits":0`, 1) + `}`,
			`{` + strings.Replace(charge, `"partnerCredits":5`, `"partnerCredits":1.5`, 1) + `}`,
			`{` + strings.Replace(charge, `"partnerCredits":5`, `"partnerCredits":"5"`, 1) + `}`,
			`{` + strings.Replace(charge, `"partnerCredits":5`, `"partnerCredits":9007199254740992`, 1) + `}`,
			`{` + strings.Replace(charge, `"relumeCredits":1`, `"relumeCredits":-1`, 1) + `}`,
			`{` + strings.Replace(charge, `"chargeId":"ch_1"`, `"chargeId":"`+strings.Repeat("c", 65)+`"`, 1) + `}`,
			`{` + charge + `,"siteId":""}`,
			`{` + charge + `,"pageId":7}`,
			`{` + strings.Replace(charge, `"description":"d"`, `"description":"`+strings.Repeat("d", 201)+`"`, 1) + `}`,
			`{"type":"credits.reverse","userId":"42","reversalId":"rv_1","partnerCredits":5,"relumeCredits":1}`,
		} {
			if rec := client.call(t, body); rec.Code != 400 || errorCode(t, rec) != "invalid_request" {
				t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
			}
		}
	})
	t.Run("counts lengths as Relume does, in UTF-16 code units", func(t *testing.T) {
		client := newTestClient(t, allCallbacks(Options{}))
		// 64 emoji are 128 UTF-16 code units: the longest userId.
		expectStatus(t, client.call(t, `{"type":"user.get","userId":"`+strings.Repeat("😀", 64)+`"}`), 200, "")
		expectStatus(t, client.call(t, `{"type":"user.get","userId":"`+strings.Repeat("😀", 64)+`x"}`), 400, "invalid_request")
	})
}

func TestEnvironment(t *testing.T) {
	var got []CallContext
	options := allCallbacks(Options{
		OnGetCredits: func(_ context.Context, _ string, call CallContext) (Credits, error) {
			got = append(got, call)
			return Credits{Balance: 3}, nil
		},
	})

	t.Run("answers a call for its own key's environment, and says which in the context", func(t *testing.T) {
		got = nil
		client := newTestClient(t, options)
		expectStatus(t, client.call(t, `{"type":"credits.get","environment":"test","userId":"42"}`), 200, "")
		if len(got) != 1 || got[0] != (CallContext{Type: CallCreditsGet, Environment: EnvironmentTest, RequestID: "req_test"}) {
			t.Errorf("context %+v", got)
		}
	})
	t.Run("refuses a correctly signed call for the other environment before any callback runs", func(t *testing.T) {
		got = nil
		live := options
		live.SecretKey = "sk_live_helpers_key"
		client := newTestClient(t, live)
		rec := postCall(t, client.CallsHandler(), live.SecretKey, testNow, `{"type":"credits.get","environment":"test","userId":"42"}`)
		expectStatus(t, rec, 400, "environment_mismatch")
		if len(got) != 0 {
			t.Error("the callback ran")
		}
	})
	t.Run("answers a call with no environment, as an older Relume deploy sends it", func(t *testing.T) {
		client := newTestClient(t, options)
		expectStatus(t, client.call(t, `{"type":"credits.get","userId":"42"}`), 200, "")
	})
	t.Run("answers 400 invalid_request for an environment it doesn't know", func(t *testing.T) {
		client := newTestClient(t, options)
		for _, env := range []string{`"staging"`, `null`, `""`, `1`} {
			expectStatus(t, client.call(t, `{"type":"credits.get","environment":`+env+`,"userId":"42"}`), 400, "invalid_request")
		}
	})
}

func TestUserGet(t *testing.T) {
	t.Run("passes the userId and returns the profile without empty fields", func(t *testing.T) {
		var gotID string
		client := newTestClient(t, Options{OnGetUser: func(_ context.Context, userID string, _ CallContext) (*UserProfile, error) {
			gotID = userID
			return &UserProfile{Name: "Ada", AvatarURL: "https://example.com/a.png"}, nil
		}})
		rec := client.call(t, `{"type":"user.get","userId":"42","extra":true}`)
		expectStatus(t, rec, 200, "")
		if gotID != "42" || rec.Body.String() != `{"name":"Ada","avatarUrl":"https://example.com/a.png"}` {
			t.Errorf("userId %q, body %s", gotID, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("headers %v", rec.Header())
		}
	})
	t.Run("answers 404 user_not_found when the callback returns nil", func(t *testing.T) {
		client := newTestClient(t, Options{OnGetUser: func(context.Context, string, CallContext) (*UserProfile, error) { return nil, nil }})
		expectStatus(t, client.call(t, `{"type":"user.get","userId":"42"}`), 404, "user_not_found")
	})
	t.Run("answers 500 for a profile the protocol refuses", func(t *testing.T) {
		for _, profile := range []UserProfile{
			{Name: strings.Repeat("n", 201)},
			{Email: "not an email"},
			{AvatarURL: "http://example.com/a.png"},
			{AvatarURL: "https://"},
		} {
			client := newTestClient(t, Options{OnGetUser: func(context.Context, string, CallContext) (*UserProfile, error) { return &profile, nil }})
			expectStatus(t, client.call(t, `{"type":"user.get","userId":"42"}`), 500, "internal_error")
			var invalid *InvalidCallbackResultError
			if len(client.errors) != 1 || !errors.As(client.errors[0], &invalid) {
				t.Errorf("%+v: handler errors %v", profile, client.errors)
			}
		}
	})
}

func TestUserGetEmails(t *testing.T) {
	raw, err := os.ReadFile("testdata/email-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct{ Valid, Invalid []string }
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, email := range vectors.Valid {
		if !isEmail(email) {
			t.Errorf("refuses %s, which Relume accepts", email)
		}
	}
	for _, email := range vectors.Invalid {
		if isEmail(email) {
			t.Errorf("sends %q, which Relume would reject", email)
		}
	}
}

func TestCreditsGet(t *testing.T) {
	t.Run("returns the balance", func(t *testing.T) {
		client := newTestClient(t, Options{OnGetCredits: func(context.Context, string, CallContext) (Credits, error) { return Credits{Balance: 0}, nil }})
		rec := client.call(t, `{"type":"credits.get","userId":"42"}`)
		if rec.Code != 200 || rec.Body.String() != `{"balance":0}` {
			t.Errorf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("answers 500 for a negative balance", func(t *testing.T) {
		client := newTestClient(t, Options{OnGetCredits: func(context.Context, string, CallContext) (Credits, error) { return Credits{Balance: -1}, nil }})
		expectStatus(t, client.call(t, `{"type":"credits.get","userId":"42"}`), 500, "internal_error")
	})
}

const chargeBody = `{"type":"credits.charge","environment":"test","userId":"42","chargeId":"ch_1","partnerCredits":80000.0,"relumeCredits":8,"action":"page.unlock","siteId":"site_1","pageId":null,"description":"Unlock","future":1}`

func TestCreditsCharge(t *testing.T) {
	t.Run("hands the callback both numbers and only known fields", func(t *testing.T) {
		var got Charge
		client := newTestClient(t, Options{OnChargeCredits: func(_ context.Context, charge Charge, _ CallContext) (ChargeResult, error) {
			got = charge
			return ChargeResult{Status: Charged, Balance: intPtr(20000)}, nil
		}})
		rec := client.call(t, chargeBody)
		want := Charge{UserID: "42", ChargeID: "ch_1", PartnerCredits: 80000, RelumeCredits: 8, Action: "page.unlock", SiteID: "site_1", Description: "Unlock"}
		if !reflect.DeepEqual(got, want) || rec.Code != 200 || rec.Body.String() != `{"balance":20000}` {
			t.Errorf("charge %+v; %d %s", got, rec.Code, rec.Body.String())
		}
	})
	t.Run("hands the callback the pages of a charge that covers several", func(t *testing.T) {
		var got Charge
		client := newTestClient(t, Options{OnChargeCredits: func(_ context.Context, charge Charge, _ CallContext) (ChargeResult, error) {
			got = charge
			return ChargeResult{Status: Charged, Balance: intPtr(0)}, nil
		}})
		several := `{"type":"credits.charge","environment":"test","userId":"42","chargeId":"ch_1","partnerCredits":240000,"relumeCredits":24,"action":"page.unlock","siteId":"site_1","pageIds":["page_1","page_2","page_3"],"description":"Build 3 pages"}`
		rec := client.call(t, several)
		want := Charge{UserID: "42", ChargeID: "ch_1", PartnerCredits: 240000, RelumeCredits: 24, Action: "page.unlock", SiteID: "site_1", PageIDs: []string{"page_1", "page_2", "page_3"}, Description: "Build 3 pages"}
		if !reflect.DeepEqual(got, want) || rec.Code != 200 {
			t.Errorf("charge %+v; %d %s", got, rec.Code, rec.Body.String())
		}
		for _, pageIDs := range []string{`[]`, `["page_1",2]`, `"page_1"`, `[""]`, `["` + strings.Repeat("p", 201) + `"]`, `[` + strings.Repeat(`"p",`, 1000) + `"p"]`} {
			body := strings.Replace(several, `["page_1","page_2","page_3"]`, pageIDs, 1)
			expectStatus(t, client.call(t, body), 400, "invalid_request")
		}
	})
	t.Run("answers 402 with the balance when the callback declines", func(t *testing.T) {
		client := newTestClient(t, Options{OnChargeCredits: func(context.Context, Charge, CallContext) (ChargeResult, error) {
			return ChargeResult{Status: Declined, Balance: intPtr(5)}, nil
		}})
		rec := client.call(t, chargeBody)
		expectStatus(t, rec, 402, "insufficient_credits")
		if decode(t, rec)["balance"] != float64(5) {
			t.Errorf("body %s", rec.Body.String())
		}
	})
	t.Run("omits a missing balance", func(t *testing.T) {
		client := newTestClient(t, Options{OnChargeCredits: func(context.Context, Charge, CallContext) (ChargeResult, error) {
			return ChargeResult{Status: Charged}, nil
		}})
		if rec := client.call(t, chargeBody); rec.Body.String() != `{}` {
			t.Errorf("body %s", rec.Body.String())
		}
	})
	t.Run("turns an error or a panic into a 500 without leaking its message", func(t *testing.T) {
		for name, callback := range map[string]func(context.Context, Charge, CallContext) (ChargeResult, error){
			"error": func(context.Context, Charge, CallContext) (ChargeResult, error) {
				return ChargeResult{}, errors.New("wallet db password is hunter2")
			},
			"panic": func(context.Context, Charge, CallContext) (ChargeResult, error) {
				panic("wallet db password is hunter2")
			},
		} {
			client := newTestClient(t, Options{OnChargeCredits: callback})
			rec := client.call(t, chargeBody)
			expectStatus(t, rec, 500, "internal_error")
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Errorf("%s: leaked %s", name, rec.Body.String())
			}
			if len(client.infos) != 1 || client.infos[0] != (HandlerErrorInfo{Route: RouteCalls, Type: CallCreditsCharge, RequestID: "req_test"}) {
				t.Errorf("%s: infos %+v", name, client.infos)
			}
		}
	})
	t.Run("answers 500 when the callback forgets the status", func(t *testing.T) {
		client := newTestClient(t, Options{OnChargeCredits: func(context.Context, Charge, CallContext) (ChargeResult, error) {
			return ChargeResult{Balance: intPtr(1)}, nil
		}})
		expectStatus(t, client.call(t, chargeBody), 500, "internal_error")
	})
}

func TestCreditsReverse(t *testing.T) {
	t.Run("accepts an unknown reason and a reversal with no chargeId", func(t *testing.T) {
		var got Reversal
		client := newTestClient(t, Options{OnReverseCharge: func(_ context.Context, reversal Reversal, _ CallContext) (ReversalResult, error) {
			got = reversal
			return ReversalResult{}, nil
		}})
		rec := client.call(t, `{"type":"credits.reverse","userId":"42","reversalId":"rv_1","partnerCredits":5,"relumeCredits":0,"reason":"goodwill"}`)
		want := Reversal{UserID: "42", ReversalID: "rv_1", PartnerCredits: 5, Reason: "goodwill"}
		if got != want || rec.Code != 200 || rec.Body.String() != `{}` {
			t.Errorf("reversal %+v; %d %s", got, rec.Code, rec.Body.String())
		}
	})
	t.Run("returns the balance when the callback gives one", func(t *testing.T) {
		client := newTestClient(t, Options{OnReverseCharge: func(context.Context, Reversal, CallContext) (ReversalResult, error) {
			return ReversalResult{Balance: intPtr(9)}, nil
		}})
		rec := client.call(t, `{"type":"credits.reverse","userId":"42","reversalId":"rv_1","partnerCredits":5,"relumeCredits":1,"chargeId":"ch_1","reason":"work_failed"}`)
		if rec.Body.String() != `{"balance":9}` {
			t.Errorf("body %s", rec.Body.String())
		}
	})
}
