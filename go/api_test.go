package relume

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

const (
	userReply   = `{"userId":"42","status":"active","createdAt":"2026-10-27T00:00:00.000Z","lastActiveAt":null,"siteCount":3}`
	siteReply   = `{"siteId":"site_1","userId":"42","name":"Bloom","createdAt":"2026-10-27T00:00:00.000Z","updatedAt":"2026-10-27T01:30:00.000Z","thumbnailUrl":null,"lastExport":null}`
	ledgerEntry = `{"id":"ch_%d","type":"charge","status":"charged","userId":"42","partnerCredits":80000,"relumeCredits":8,"action":"page.unlock","siteId":"site_1","pageId":null,"chargeId":null,"reason":null,"createdAt":"2026-10-27T01:30:00.000Z","resolvedAt":"2026-10-27T01:30:01.000Z"}`
)

func entry(n string) string { return strings.Replace(ledgerEntry, "%d", n, 1) }

func page(cursor string, hasMore bool, items ...string) func(w http.ResponseWriter) {
	more := "false"
	if hasMore {
		more = "true"
	}
	return reply(200, `{"data":[`+strings.Join(items, ",")+`],"nextCursor":"`+cursor+`","hasMore":`+more+`}`)
}

func apiCode(err error) string {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func TestEndpoints(t *testing.T) {
	t.Run("GetUser, DeactivateUser and ReactivateUser", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, userReply), reply(200, userReply), reply(200, userReply))
		client := newAPIClient(t, api, Options{})
		user, err := client.GetUser(ctx, "42")
		if err != nil || user.UserID != "42" || user.Status != UserActive || user.LastActiveAt != nil || user.SiteCount != 3 {
			t.Fatalf("%+v %v", user, err)
		}
		if _, err := client.DeactivateUser(ctx, "42"); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ReactivateUser(ctx, "42"); err != nil {
			t.Fatal(err)
		}
		got := api.got()
		for i, want := range []string{"GET /users/42", "POST /users/42/deactivate", "POST /users/42/reactivate"} {
			if got[i].method+" "+got[i].path != want {
				t.Errorf("request %d: %s %s, want %s", i, got[i].method, got[i].path, want)
			}
		}
	})
	t.Run("CreateSite, GetSite and ListSites", func(t *testing.T) {
		api := newFakeAPI(t, reply(201, `{"siteId":"site_1"}`), reply(201, `{"siteId":"site_2"}`), reply(200, siteReply), page("c1", false, siteReply))
		client := newAPIClient(t, api, Options{})
		created, err := client.CreateSite(ctx, "42", CreateSiteParams{Name: "Bloom"})
		if err != nil || created.SiteID != "site_1" {
			t.Fatalf("%+v %v", created, err)
		}
		if _, err := client.CreateSite(ctx, "42", CreateSiteParams{}); err != nil {
			t.Fatal(err)
		}
		site, err := client.GetSite(ctx, "site_1")
		if err != nil || site.Name != "Bloom" || site.ThumbnailURL != nil || site.UpdatedAt.Minute() != 30 {
			t.Fatalf("%+v %v", site, err)
		}
		sites, err := client.ListSites(ctx, "42", PageQuery{Limit: 10}).Collect()
		if err != nil || len(sites) != 1 {
			t.Fatalf("%+v %v", sites, err)
		}
		got := api.got()
		if got[0].body != `{"name":"Bloom"}` || got[1].body != `{}` || got[1].header.Get("Content-Type") != "application/json" {
			t.Errorf("create bodies %q %q", got[0].body, got[1].body)
		}
		if got[3].path != "/users/42/sites" || got[3].query != "limit=10" {
			t.Errorf("list %s?%s", got[3].path, got[3].query)
		}
	})
	t.Run("GetExport", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, `{"exportId":"exp_1","siteId":"site_1","status":"ready","downloadUrl":"https://x/y.zip","createdAt":"2026-10-27T01:30:00.000Z","expiresAt":"2026-10-28T01:30:00.000Z"}`), apiError(501, "not_implemented"))
		client := newAPIClient(t, api, Options{})
		export, err := client.GetExport(ctx, "site_1", "exp_1")
		if err != nil || export.Status != ExportReady || *export.DownloadURL != "https://x/y.zip" || api.got()[0].path != "/sites/site_1/exports/exp_1" {
			t.Fatalf("%+v %v", export, err)
		}
		if _, err := client.GetExport(ctx, "site_1", "exp_1"); apiCode(err) != CodeNotImplemented {
			t.Errorf("got %v", err)
		}
	})
	t.Run("GetLedgerEntry, with PartnerResponse", func(t *testing.T) {
		detail := strings.TrimSuffix(entry("1"), "}") + `,"partnerResponse":{"httpStatus":200,"body":{"balance":192},"attempts":1}}`
		api := newFakeAPI(t, reply(200, detail))
		client := newAPIClient(t, api, Options{})
		got, err := client.GetLedgerEntry(ctx, "ch_1")
		if err != nil || got.ID != "ch_1" || got.Status != LedgerCharged || *got.Action != "page.unlock" || got.PageID != nil ||
			*got.PartnerResponse.HTTPStatus != 200 || *got.PartnerResponse.Body.Balance != 192 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("Usage sends the range and joins GroupBy, or leaves it out", func(t *testing.T) {
		row := `{"day":"2026-10-27","action":null,"partnerCredits":{"charged":2,"reversed":1,"net":1},"relumeCredits":{"charged":16,"reversed":8,"net":8}}`
		api := newFakeAPI(t, page("u1", false, row), page("u2", false))
		client := newAPIClient(t, api, Options{})
		rows, err := client.Usage(ctx, UsageParams{From: "2026-10-01", To: "2026-11-01", GroupBy: []UsageGroup{UsageByDay, UsageByAction}}).Collect()
		if err != nil || len(rows) != 1 || rows[0].Day != "2026-10-27" || rows[0].Action != nil || rows[0].RelumeCredits.Net != 8 {
			t.Fatalf("%+v %v", rows, err)
		}
		if _, err := client.Usage(ctx, UsageParams{From: "2026-10-01", To: "2026-11-01"}).Collect(); err != nil {
			t.Fatal(err)
		}
		got := api.got()
		if got[0].query != "from=2026-10-01&groupBy=day%2Caction&to=2026-11-01" || got[1].query != "from=2026-10-01&to=2026-11-01" {
			t.Errorf("queries %q %q", got[0].query, got[1].query)
		}
	})
	t.Run("GetPrices returns the prices and what credits are called", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, `{"data":[{"action":"chat.message","partnerCredits":null,"relumeCredits":1}],"creditUnit":{"singular":"coin","plural":"coins","iconUrl":"https://example.com/coin.svg"}}`))
		client := newAPIClient(t, api, Options{})
		list, err := client.GetPrices(ctx)
		if err != nil || len(list.Data) != 1 || list.CreditUnit != (CreditUnit{Singular: "coin", Plural: "coins", IconURL: "https://example.com/coin.svg"}) {
			t.Fatalf("%+v %v", list, err)
		}
	})
	t.Run("answers unexpected_response for prices without what credits are called", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, `{"data":[]}`))
		client := newAPIClient(t, api, Options{})
		_, err := client.GetPrices(ctx)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != 200 || apiErr.Code != CodeUnexpectedResponse {
			t.Fatalf("%v", err)
		}
	})
	t.Run("ListPrices returns the prices", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, `{"data":[{"action":"chat.message","partnerCredits":null,"relumeCredits":1}],"creditUnit":{"singular":"credit","plural":"credits"}}`))
		client := newAPIClient(t, api, Options{})
		prices, err := client.ListPrices(ctx)
		if err != nil || len(prices) != 1 || prices[0].PartnerCredits != nil || prices[0].RelumeCredits != 1 {
			t.Fatalf("%+v %v", prices, err)
		}
	})
	t.Run("escapes ids in the path and refuses empty ones and dot segments", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, userReply), reply(200, userReply))
		client := newAPIClient(t, api, Options{})
		if _, err := client.GetUser(ctx, "a/b c?d#e+f"); err != nil {
			t.Fatal(err)
		}
		if _, err := client.GetUser(ctx, "..."); err != nil {
			t.Fatal(err)
		}
		got := api.got()
		if got[0].path != "/users/a%2Fb%20c%3Fd%23e%2Bf" || got[1].path != "/users/..." {
			t.Errorf("paths %s %s", got[0].path, got[1].path)
		}
		for _, id := range []string{"", ".", ".."} {
			if _, err := client.GetUser(ctx, id); err == nil {
				t.Errorf("GetUser(%q) didn't refuse", id)
			}
			if _, err := client.DeactivateUser(ctx, id); err == nil {
				t.Errorf("DeactivateUser(%q) didn't refuse", id)
			}
			if _, err := client.GetExport(ctx, "site_1", id); err == nil {
				t.Errorf("GetExport(%q) didn't refuse", id)
			}
			if _, err := client.ListSites(ctx, id, PageQuery{}).Collect(); err == nil {
				t.Errorf("ListSites(%q) didn't refuse", id)
			}
		}
		if len(api.got()) != 2 {
			t.Error("sent a request for a refused id")
		}
	})
}

func TestLists(t *testing.T) {
	t.Run("ranges over the whole ledger, a page at a time", func(t *testing.T) {
		api := newFakeAPI(t, page("c1", true, entry("1"), entry("2")), page("c2", false, entry("3")))
		client := newAPIClient(t, api, Options{})
		list := client.ListLedger(ctx, PageQuery{})
		var ids []string
		for entry, err := range list.All() {
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, entry.ID)
		}
		got := api.got()
		if strings.Join(ids, ",") != "ch_1,ch_2,ch_3" || list.NextCursor() != "c2" || got[0].query != "" || got[1].query != "after=c1" {
			t.Errorf("ids %v, cursor %q, queries %q %q", ids, list.NextCursor(), got[0].query, got[1].query)
		}
	})
	t.Run("resumes from a stored cursor with only what is new", func(t *testing.T) {
		api := newFakeAPI(t, page("c3", false, entry("4")))
		client := newAPIClient(t, api, Options{})
		list := client.ListLedger(ctx, PageQuery{After: "c2", Limit: 100})
		if list.NextCursor() != "c2" {
			t.Errorf("cursor before iterating %q", list.NextCursor())
		}
		entries, err := list.Collect()
		if err != nil || len(entries) != 1 || list.NextCursor() != "c3" || api.got()[0].query != "after=c2&limit=100" {
			t.Errorf("%v %v %q %q", entries, err, list.NextCursor(), api.got()[0].query)
		}
	})
	t.Run("doesn't advance NextCursor past a page that wasn't finished", func(t *testing.T) {
		api := newFakeAPI(t, page("c1", true, entry("1"), entry("2")))
		client := newAPIClient(t, api, Options{})
		list := client.ListLedger(ctx, PageQuery{After: "c0"})
		for range list.All() {
			break
		}
		if list.NextCursor() != "c0" {
			t.Errorf("cursor %q", list.NextCursor())
		}
	})
	t.Run("Pages hands out a page and its cursor at a time", func(t *testing.T) {
		api := newFakeAPI(t, page("c1", true, entry("1")), page("c2", false, entry("2")))
		client := newAPIClient(t, api, Options{})
		list := client.ListLedger(ctx, PageQuery{})
		var cursors []string
		for page, err := range list.Pages() {
			if err != nil {
				t.Fatal(err)
			}
			// The cursor moves when the next page is asked for.
			cursors = append(cursors, page.NextCursor+"@"+list.NextCursor())
		}
		if strings.Join(cursors, ",") != "c1@,c2@c1" || list.NextCursor() != "c2" {
			t.Errorf("cursors %v, final %q", cursors, list.NextCursor())
		}
	})
	t.Run("requests nothing until iterated", func(t *testing.T) {
		api := newFakeAPI(t)
		client := newAPIClient(t, api, Options{})
		client.ListLedger(ctx, PageQuery{})
		if len(api.got()) != 0 {
			t.Error("sent a request")
		}
	})
	t.Run("fails when a page says hasMore without moving the cursor", func(t *testing.T) {
		api := newFakeAPI(t, page("c1", true, entry("1")))
		client := newAPIClient(t, api, Options{})
		if _, err := client.ListLedger(ctx, PageQuery{After: "c1"}).Collect(); apiCode(err) != CodeUnexpectedResponse {
			t.Errorf("got %v", err)
		}
	})
	t.Run("passes invalid_cursor through", func(t *testing.T) {
		api := newFakeAPI(t, apiError(400, "invalid_cursor"))
		client := newAPIClient(t, api, Options{})
		if _, err := client.ListLedger(ctx, PageQuery{After: "junk"}).Collect(); apiCode(err) != CodeInvalidCursor {
			t.Errorf("got %v", err)
		}
	})
	t.Run("refuses a page without nextCursor or hasMore", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, `{"data":[]}`))
		client := newAPIClient(t, api, Options{})
		if _, err := client.ListLedger(ctx, PageQuery{}).Collect(); apiCode(err) != CodeUnexpectedResponse {
			t.Errorf("got %v", err)
		}
	})
}

func TestErrors(t *testing.T) {
	t.Run("returns an *Error with the API code, status and request id", func(t *testing.T) {
		api := newFakeAPI(t, apiError(404, "user_not_found"))
		client := newAPIClient(t, api, Options{})
		_, err := client.GetUser(ctx, "42")
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != CodeUserNotFound || apiErr.RequestID != "req_404" || apiErr.Message != "Refused: user_not_found." {
			t.Errorf("got %#v", err)
		}
	})
	t.Run("returns unexpected_response for a 2xx without the object", func(t *testing.T) {
		api := newFakeAPI(t, reply(200, `{"userId":"42"}`), reply(200, `not json`), reply(200, `{"userId":"42","status":"active","siteCount":"3"}`))
		client := newAPIClient(t, api, Options{})
		for range 3 {
			if _, err := client.GetUser(ctx, "42"); apiCode(err) != CodeUnexpectedResponse {
				t.Errorf("got %v", err)
			}
		}
	})
	t.Run("returns unexpected_response for an error without a JSON body", func(t *testing.T) {
		api := newFakeAPI(t, reply(418, `<html>`))
		client := newAPIClient(t, api, Options{})
		_, err := client.GetUser(ctx, "42")
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != 418 || apiErr.Code != CodeUnexpectedResponse {
			t.Errorf("got %v", err)
		}
	})
}

func TestRetries(t *testing.T) {
	t.Run("retries a 429 after Retry-After", func(t *testing.T) {
		api := newFakeAPI(t, reply(429, `{"error":{"code":"rate_limited","message":"Slow down."}}`, "Retry-After", "3"), reply(200, userReply))
		client := newAPIClient(t, api, Options{})
		if _, err := client.GetUser(ctx, "42"); err != nil || len(client.sleeps) != 1 || client.sleeps[0] != 3*time.Second {
			t.Errorf("%v, sleeps %v", err, client.sleeps)
		}
	})
	t.Run("reads Retry-After as an HTTP date too", func(t *testing.T) {
		date := testNow.Add(7 * time.Second).UTC().Format(http.TimeFormat)
		api := newFakeAPI(t, reply(429, `{}`, "Retry-After", date), reply(200, userReply))
		client := newAPIClient(t, api, Options{})
		if _, err := client.GetUser(ctx, "42"); err != nil || client.sleeps[0] != 7*time.Second {
			t.Errorf("%v, sleeps %v", err, client.sleeps)
		}
	})
	t.Run("retries a 5xx with growing, capped backoff", func(t *testing.T) {
		api := newFakeAPI(t, reply(503, ``), reply(502, ``), reply(500, ``), reply(504, ``), reply(200, userReply))
		client := newAPIClient(t, api, Options{MaxRetries: 4})
		if _, err := client.GetUser(ctx, "42"); err != nil || len(client.sleeps) != 4 {
			t.Fatalf("%v, sleeps %v", err, client.sleeps)
		}
		for i, sleep := range client.sleeps {
			if ceiling := retryBaseDelay << i; sleep < 0 || sleep > ceiling {
				t.Errorf("sleep %d: %s, over %s", i, sleep, ceiling)
			}
		}
		for attempt := range 20 {
			if delay := backoff(attempt); delay > retryMaxDelay {
				t.Errorf("attempt %d: %s", attempt, delay)
			}
		}
	})
	t.Run("caps a 5xx's Retry-After rather than giving up", func(t *testing.T) {
		api := newFakeAPI(t, reply(503, ``, "Retry-After", "3600"), reply(200, userReply))
		client := newAPIClient(t, api, Options{})
		if _, err := client.GetUser(ctx, "42"); err != nil || client.sleeps[0] != retryMaxDelay {
			t.Errorf("%v, sleeps %v", err, client.sleeps)
		}
	})
	t.Run("retries Cloudflare's 52x answers", func(t *testing.T) {
		api := newFakeAPI(t, reply(522, ``), reply(200, userReply))
		client := newAPIClient(t, api, Options{})
		if _, err := client.GetUser(ctx, "42"); err != nil {
			t.Error(err)
		}
	})
	t.Run("returns the last error once retries run out, and honours MaxRetries", func(t *testing.T) {
		for _, tc := range []struct {
			options  Options
			attempts int
		}{
			{Options{}, 3},
			{Options{MaxRetries: 1}, 2},
			{Options{DisableRetries: true}, 1},
		} {
			replies := make([]func(http.ResponseWriter), tc.attempts)
			for i := range replies {
				replies[i] = apiError(503, "internal_error")
			}
			api := newFakeAPI(t, replies...)
			client := newAPIClient(t, api, tc.options)
			_, err := client.GetUser(ctx, "42")
			if apiCode(err) != CodeInternalError || len(api.got()) != tc.attempts {
				t.Errorf("%+v: %v after %d requests", tc.options, err, len(api.got()))
			}
		}
	})
	t.Run("doesn't retry a 4xx, or a 501", func(t *testing.T) {
		for _, status := range []int{400, 401, 403, 404, 501, 505} {
			api := newFakeAPI(t, reply(status, `{}`))
			client := newAPIClient(t, api, Options{})
			if _, err := client.GetUser(ctx, "42"); err == nil || len(api.got()) != 1 {
				t.Errorf("%d: %v after %d requests", status, err, len(api.got()))
			}
		}
	})
	t.Run("doesn't wait out a Retry-After over a minute", func(t *testing.T) {
		api := newFakeAPI(t, reply(429, `{"error":{"code":"rate_limited","message":"Later."}}`, "Retry-After", "61"))
		client := newAPIClient(t, api, Options{})
		if _, err := client.GetUser(ctx, "42"); apiCode(err) != CodeRateLimited || len(client.sleeps) != 0 {
			t.Errorf("%v, sleeps %v", err, client.sleeps)
		}
	})
	t.Run("CreateSite retries a 429 but not a 5xx", func(t *testing.T) {
		api := newFakeAPI(t, reply(429, `{}`, "Retry-After", "1"), reply(503, `{}`))
		client := newAPIClient(t, api, Options{})
		if _, err := client.CreateSite(ctx, "42", CreateSiteParams{}); err == nil || len(api.got()) != 2 {
			t.Errorf("%v after %d requests", err, len(api.got()))
		}
	})
	t.Run("retries a failed connection, except on CreateSite", func(t *testing.T) {
		api := newFakeAPI(t)
		api.Close()
		client := newAPIClient(t, api, Options{})
		if _, err := client.GetUser(ctx, "42"); apiCode(err) != CodeNetworkError || len(client.sleeps) != 2 {
			t.Errorf("GetUser: %v, sleeps %v", err, client.sleeps)
		}
		client.sleeps = nil
		if _, err := client.CreateSite(ctx, "42", CreateSiteParams{}); apiCode(err) != CodeNetworkError || len(client.sleeps) != 0 {
			t.Errorf("CreateSite: %v, sleeps %v", err, client.sleeps)
		}
	})
	t.Run("treats a redirect as a network error, without following or retrying it", func(t *testing.T) {
		api := newFakeAPI(t, reply(302, ``, "Location", "https://evil.example/"))
		client := newAPIClient(t, api, Options{})
		_, err := client.GetUser(ctx, "42")
		if apiCode(err) != CodeNetworkError || !strings.Contains(err.Error(), "redirect") || len(api.got()) != 1 {
			t.Errorf("%v after %d requests", err, len(api.got()))
		}
	})
	t.Run("stops when the caller's context is done", func(t *testing.T) {
		api := newFakeAPI(t, reply(503, ``))
		client := newAPIClient(t, api, Options{})
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := client.GetUser(cancelled, "42"); !errors.Is(err, context.Canceled) || len(client.sleeps) != 0 {
			t.Errorf("%v, sleeps %v", err, client.sleeps)
		}
	})
	t.Run("retries a page of a list, then carries on", func(t *testing.T) {
		api := newFakeAPI(t, page("c1", true, entry("1")), reply(503, ``), page("c2", false, entry("2")))
		client := newAPIClient(t, api, Options{})
		entries, err := client.ListLedger(ctx, PageQuery{}).Collect()
		got := api.got()
		if err != nil || len(entries) != 2 || got[1].query != "after=c1" || got[2].query != "after=c1" {
			t.Errorf("%v %v", entries, err)
		}
	})
}
