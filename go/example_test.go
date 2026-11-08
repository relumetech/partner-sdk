package relume_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"

	relume "github.com/relumetech/partner-sdk/go"
)

// Your own user store and wallet.
type wallet interface {
	Balance(ctx context.Context, userID string) (int, error)
	// Debit at most once per ref; ok is false when the user can't afford it.
	Debit(ctx context.Context, userID string, credits int, ref string) (balance int, ok bool, err error)
	// Credit at most once per ref; a reversal of a debit never made changes
	// nothing.
	Credit(ctx context.Context, userID string, credits int, ref, reverses string) (balance int, err error)
}

var (
	users interface {
		Find(ctx context.Context, userID string) (name, email string, found bool, err error)
	}
	db             wallet
	signedInUserID func(r *http.Request) (string, error)
)

func Example() {
	client, err := relume.New(relume.Options{
		SecretKey: os.Getenv("RELUME_SECRET_KEY"),

		// Who is signed in, for the builder's GET /relume/session.
		UserIDFromRequest: signedInUserID,

		OnGetUser: func(ctx context.Context, userID string, _ relume.CallContext) (*relume.UserProfile, error) {
			name, email, found, err := users.Find(ctx, userID)
			if err != nil || !found {
				return nil, err // nil profile → 404
			}
			return &relume.UserProfile{Name: name, Email: email}, nil
		},
		OnGetCredits: func(ctx context.Context, userID string, _ relume.CallContext) (relume.Credits, error) {
			balance, err := db.Balance(ctx, userID)
			return relume.Credits{Balance: balance}, err
		},
		OnChargeCredits: func(ctx context.Context, charge relume.Charge, _ relume.CallContext) (relume.ChargeResult, error) {
			balance, ok, err := db.Debit(ctx, charge.UserID, charge.PartnerCredits, charge.ChargeID)
			if err != nil {
				return relume.ChargeResult{}, err
			}
			status := relume.Charged
			if !ok {
				status = relume.Declined
			}
			return relume.ChargeResult{Status: status, Balance: &balance}, nil
		},
		OnReverseCharge: func(ctx context.Context, reversal relume.Reversal, _ relume.CallContext) (relume.ReversalResult, error) {
			balance, err := db.Credit(ctx, reversal.UserID, reversal.PartnerCredits, reversal.ReversalID, reversal.ChargeID)
			return relume.ReversalResult{Balance: &balance}, err
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// GET /relume/session and POST /relume/calls. Set your endpoint URL in
	// Relume's partner settings to https://<your backend>/relume/calls.
	http.Handle("/relume/", client.Handler())
}

func ExampleClient_ListLedger() {
	client, _ := relume.New(relume.Options{SecretKey: os.Getenv("RELUME_SECRET_KEY")})
	ctx := context.Background()

	stored := "" // the cursor you saved last time
	list := client.ListLedger(ctx, relume.PageQuery{After: stored})
	for entry, err := range list.All() {
		if err != nil {
			log.Fatal(err)
		}
		log.Println(entry.ID, entry.Status) // upsert on entry.ID
	}
	stored = list.NextCursor() // save it for next time
}

func ExampleError() {
	client, _ := relume.New(relume.Options{SecretKey: os.Getenv("RELUME_SECRET_KEY")})

	_, err := client.DeactivateUser(context.Background(), "42")
	var apiErr *relume.Error
	if errors.As(err, &apiErr) && apiErr.Code == relume.CodeUserNotFound {
		log.Println("no Relume account yet: nothing to deactivate")
	}
}
