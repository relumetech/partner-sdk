# relume (Go)

The Go server SDK for Relume partners. It answers Relume's signed calls,
serves builder sessions and calls the partner API, so your backend never
writes any cryptography.

It implements the server side of the Relume Partner Protocol v1 and is the Go
counterpart of
[`@relume_io/partner-node`](https://www.npmjs.com/package/@relume_io/partner-node).
Pair it with
[`@relume_io/partner-embed`](https://www.npmjs.com/package/@relume_io/partner-embed)
on your page. The [partner docs](https://apis.relume.io/nova/docs/partners)
have guides and the partner API reference.

## Install

```sh
go get github.com/relumetech/partner-sdk/go
```

Go 1.23 or later. Standard library only.

## Quickstart (net/http)

```go
import relume "github.com/relumetech/partner-sdk/go"

client, err := relume.New(relume.Options{
	SecretKey: os.Getenv("RELUME_SECRET_KEY"),

	// Who is signed in, for the builder's GET /relume/session. "" for nobody.
	UserIDFromRequest: func(r *http.Request) (string, error) {
		return sessions.UserID(r), nil
	},

	OnGetUser: func(ctx context.Context, userID string, _ relume.CallContext) (*relume.UserProfile, error) {
		user, err := db.FindUser(ctx, userID)
		if err != nil || user == nil {
			return nil, err // nil → 404
		}
		return &relume.UserProfile{Name: user.Name, Email: user.Email}, nil
	},
	OnGetCredits: func(ctx context.Context, userID string, _ relume.CallContext) (relume.Credits, error) {
		balance, err := wallet.Balance(ctx, userID)
		return relume.Credits{Balance: balance}, err
	},
	OnChargeCredits: func(ctx context.Context, c relume.Charge, _ relume.CallContext) (relume.ChargeResult, error) {
		balance, ok, err := wallet.Debit(ctx, c.UserID, c.PartnerCredits, c.ChargeID)
		if err != nil {
			return relume.ChargeResult{}, err
		}
		status := relume.Charged
		if !ok {
			status = relume.Declined
		}
		return relume.ChargeResult{Status: status, Balance: &balance}, nil
	},
	OnReverseCharge: func(ctx context.Context, r relume.Reversal, _ relume.CallContext) (relume.ReversalResult, error) {
		balance, err := wallet.Credit(ctx, r.UserID, r.PartnerCredits, r.ReversalID, r.ChargeID)
		return relume.ReversalResult{Balance: &balance}, err
	},
})
if err != nil {
	log.Fatal(err)
}

http.Handle("/relume/", client.Handler())
```

Then, in Relume's partner settings, set your endpoint URL to
`https://<your backend>/relume/calls`.

## What it serves

| Handler                   | Route                 | Called by                  | Does                                                                                                                             |
| ------------------------- | --------------------- | -------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `client.SessionHandler()` | `GET /relume/session` | `@relume_io/partner-embed` | Refuses another site, resolves the user with `UserIDFromRequest`, calls `POST /sessions`, returns `{ clientSecret, expiresAt }`. |
| `client.CallsHandler()`   | `POST /relume/calls`  | Relume                     | Verifies `Relume-Signature` and the 5-minute window, routes by `type`, runs your callback, checks what it returns.               |

`client.Handler()` serves both, by the last segment of the path (`…/session`,
`…/calls`), so mount it under a prefix of its own. With a router that matches
methods, mount the two handlers separately instead:

```go
mux.Handle("GET /relume/session", client.SessionHandler())
mux.Handle("POST /relume/calls", client.CallsHandler())
```

They are plain `http.Handler`s, so any router takes them: `gin.WrapH`,
`echo.WrapHandler`, chi's `r.Method(…)`. `CallsHandler` reads the raw body
itself, because the signature covers the exact bytes: don't put a
body-parsing middleware in front of it.

CORS preflights (`OPTIONS`) are left to your CORS middleware in front of the
handlers; one that reaches them gets `204` with no CORS headers. If your
backend is on another origin from your page, pass `sessionUrl` to
`Relume.init` in the embed and allow your page's origin, with credentials and
the `Relume-Embed` header, in that route's CORS response. Use an exact
allowlist: never reflect the request `Origin` and never use `*`.

Any site can make a signed-in user's browser call the session route, so it
refuses two things before it looks at the user: a request with
`Sec-Fetch-Site: cross-site`, which only another site's page sends, and one
without the `Relume-Embed: 1` header `@relume_io/partner-embed` sends. Neither
helps against a script on your own page: load only scripts you trust where the
builder is embedded.

| Situation                                     | Answer                                                       |
| --------------------------------------------- | ------------------------------------------------------------ |
| Bad or missing signature, or stale `t`        | `401 invalid_signature`; your callback isn't called          |
| Unknown `type`, or no callback set            | `400 unsupported_call_type`                                  |
| Body that isn't valid for its `type`          | `400 invalid_request`                                        |
| `environment` isn't your secret key's         | `400 environment_mismatch`; your callback isn't called       |
| `OnGetUser` returns `nil`                     | `404 user_not_found`                                         |
| `OnChargeCredits` returns `Status: Declined`  | `402 insufficient_credits`, with `balance`                   |
| A callback errors, panics or returns bad data | `500 internal_error`, and `OnHandlerError` gets the error    |
| No `Relume-Embed: 1` header (session route)   | `400 embed_header_missing`, and `OnHandlerError` says why    |
| `Sec-Fetch-Site: cross-site` (session route)  | `403 cross_site_request`                                     |
| Nobody signed in (session route)              | `401 not_signed_in`                                          |
| `POST /sessions` refused (session route)      | Relume's status and `{ error }`, e.g. `403 user_deactivated` |

Every response is JSON with `Cache-Control: no-store`.

## The callbacks

Each callback gets the request's `context.Context` and a `CallContext`
(`Type`, `Environment`, `RequestID`). Quote the `RequestID` to Relume support.
`Environment` is always your secret key's: a call for the other environment is
refused before a callback runs, so a test call can never move a live wallet.
Still give test and live their own endpoint URLs, each with its own client.

**`OnChargeCredits`**

- **Debit `PartnerCredits` only.** `RelumeCredits` is what Relume charges you
  for the action, for your records. Never debit it from the user's wallet.
- **Idempotent on `ChargeID`.** Relume retries a charge with the same
  `ChargeID`. Debit at most once, and answer a repeat with the same result.
- Return `Status: relume.Declined` when the user can't afford it; the builder
  asks them to top up. `Status` has no valid zero value, so a result that
  forgets it is answered `500` rather than read as either outcome.
- When a user builds several pages at once, one `page.unlock` charge covers
  them all: `PageIDs` in place of `PageID`, both prices multiplied by the
  number of pages. Debit it as any other charge. If one page can't be
  delivered, Relume reverses that page's share with the same `ChargeID`.
- Returning an error (or timing out) makes the outcome unknown. Relume then
  never does the work and always sends `credits.reverse` for that `ChargeID`.

**`OnReverseCharge`**

- **Credit back `PartnerCredits` only**, at most once per `ReversalID`.
- If `ChargeID` names a charge you never debited, credit nothing, return
  normally, and record that `ChargeID` as declined: an attempt can still reach
  your wallet after the reversal, and `OnChargeCredits` must then decline it
  without debiting.
- `ChargeID` is `""` for a staff refund. `Reason` is `work_failed`,
  `charge_unknown` or `staff_refund`; accept new values.
- Accept reversals for users you have since deactivated.

**`OnGetUser`**: `Name` at most 200 characters, `Email` a valid address,
`AvatarURL` an `https` URL; `""` leaves a field out. Return `nil` for an
unknown user.

**`OnGetCredits`**: a whole number of your own credits, zero or more.

**`UserIDFromRequest`**: read the user from your own session. Never take a
user id from the query string or body.

## The partner API

Every endpoint is a method on the client, and each returns an `*relume.Error`
when the API refuses. Switch on its `Code`:

```go
user, err := client.DeactivateUser(ctx, "42")
var apiErr *relume.Error
if errors.As(err, &apiErr) && apiErr.Code == relume.CodeUserNotFound {
	// No Relume account yet.
}
```

| Method                            | Endpoint                                 |
| --------------------------------- | ---------------------------------------- |
| `CreateSession(ctx, userID)`      | `POST /sessions`                         |
| `GetUser(ctx, userID)`            | `GET /users/{userId}`                    |
| `DeactivateUser(ctx, userID)`     | `POST /users/{userId}/deactivate`        |
| `ReactivateUser(ctx, userID)`     | `POST /users/{userId}/reactivate`        |
| `ListSites(ctx, userID, query)`   | `GET /users/{userId}/sites`              |
| `CreateSite(ctx, userID, params)` | `POST /users/{userId}/sites`             |
| `GetSite(ctx, siteID)`            | `GET /sites/{siteId}`                    |
| `GetExport(ctx, siteID, id)`      | `GET /sites/{siteId}/exports/{exportId}` |
| `ListLedger(ctx, query)`          | `GET /ledger`                            |
| `GetLedgerEntry(ctx, id)`         | `GET /ledger/{id}`                       |
| `Usage(ctx, params)`              | `GET /usage`                             |
| `ListPrices(ctx)`                 | `GET /prices`                            |
| `GetPrices(ctx)`                  | `GET /prices`, with `CreditUnit`         |

Lists are fetched a page at a time as you range over them:

```go
list := client.ListLedger(ctx, relume.PageQuery{After: stored})
for entry, err := range list.All() {
	if err != nil {
		return err
	}
	upsert(entry) // on entry.ID: an entry comes again each time its status changes
}
stored = list.NextCursor()
```

Range over `list.Pages()` to checkpoint a page at a time. `NextCursor` only
moves past a page once all its items were handed out.

Requests carry the secret key, so `APIBaseURL` must be `https` (http only for
localhost). A 429 is retried after its `Retry-After`; a 5xx or a failed
connection with backoff, except on `CreateSite`, which isn't idempotent.
Redirects are never followed.

## Options

```go
relume.New(relume.Options{
	SecretKey:      "sk_test_…",       // required; sk_test_… or sk_live_…
	APIBaseURL:     "https://…",       // default production; Relume gives you staging's
	APITimeout:     15 * time.Second,  // per attempt
	MaxRetries:     2,                 // retries after the first attempt; default 2
	DisableRetries: false,             // true for a single attempt per request
	HTTPClient:     proxiedClient,     // default http.DefaultClient
	OnHandlerError: func(err error, info relume.HandlerErrorInfo) {}, // default slog.Error
	// …and the callbacks above.
})
```

## Testing your endpoint

`relume.SignCall` signs a body the way Relume does, for your own tests. To
check a running endpoint against the whole protocol, run the checker from the
Node SDK; it works against any language:

```sh
RELUME_SECRET_KEY=sk_test_… npx @relume_io/partner-node check \
  --url https://partner.example/relume/calls --user <a test user id>
```

## Developing

`testdata/` holds the protocol's signing and email test vectors, generated
from Relume's backend. They are copies of `docs/partner-protocol/test-vectors.json`
and `email-vectors.json` in nova: copy them over again when the protocol changes.

```sh
go vet ./... && go test -race ./...
```

CI runs these on every push, plus `gofmt` and a 32-bit `go vet`.

### Releasing

1. If the protocol changed, copy the test vectors over (above).
2. Set `Version` in `relume.go` to the new version and merge to `main`.
3. Tag the merge commit and push the tag. The module is in `go/`, so the tag
   needs the prefix:

   ```sh
   git fetch origin && git tag go/v1.2.3 origin/main && git push origin go/v1.2.3
   ```

4. Have the Go proxy fetch it, so pkg.go.dev lists it:

   ```sh
   GOPROXY=https://proxy.golang.org go list -m github.com/relumetech/partner-sdk/go@v1.2.3
   ```

CI fails a tag that doesn't match `Version`, but by then the tag is already
public. A published version can't be changed or deleted: withdraw a bad one
with a `retract` directive in the next release.
