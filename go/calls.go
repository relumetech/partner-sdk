package relume

// The four calls Relume makes to a partner (Relume Partner Protocol v1,
// section 5): parsing the signed body, running the callback and checking
// what it returns. Unknown request fields are dropped (the protocol may add
// fields at any time); unknown action and reason values are accepted. A call
// for the other environment is refused, so a test call can never move a
// live wallet.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

type CallType string

const (
	CallUserGet        CallType = "user.get"
	CallCreditsGet     CallType = "credits.get"
	CallCreditsCharge  CallType = "credits.charge"
	CallCreditsReverse CallType = "credits.reverse"
)

// CallContext is passed to every callback, for your logs.
type CallContext struct {
	Type CallType
	// Always your secret key's: a call for the other environment is refused
	// before any callback runs.
	Environment Environment
	// Relume-Request-Id, unique per attempt. Quote it to Relume support.
	RequestID string
}

// UserProfile is what OnGetUser returns. Every field is optional; "" is unset.
type UserProfile struct {
	// At most 200 characters.
	Name string `json:"name,omitempty"`
	// A valid email address.
	Email string `json:"email,omitempty"`
	// An https URL.
	AvatarURL string `json:"avatarUrl,omitempty"`
}

// Credits is what OnGetCredits returns.
type Credits struct {
	// The user's balance in your own credits (the unit of PartnerCredits),
	// zero or more.
	Balance int
}

type Charge struct {
	// Your own id for the user.
	UserID string
	// Idempotency key. Debit at most once per ChargeID, and answer a repeat
	// with the same result as the first time.
	ChargeID string
	// What to debit from the user's wallet. At least 1.
	PartnerCredits int
	// What Relume charges you for this action, for your records only. Never
	// debit it from the wallet.
	RelumeCredits int
	// The billable action, e.g. "page.unlock". New actions may appear.
	Action string
	// "" when the work isn't for a site or page.
	SiteID string
	PageID string
	// The pages one charge pays for, when it covers several (a page.unlock
	// for each, with both prices multiplied by their count). Sent instead of
	// PageID; nil otherwise. Debit PartnerCredits as for any charge.
	PageIDs []string
	// For the user's wallet statement, e.g. "Build 3 pages". At most 200
	// characters.
	Description string
}

// ChargeStatus has no valid zero value, so a ChargeResult that forgets to
// say whether the wallet was debited is answered 500 rather than read as
// either outcome.
type ChargeStatus int

const (
	_ ChargeStatus = iota
	// Charged: the wallet was debited.
	Charged
	// Declined: the user can't afford it. Sent as 402; the builder asks them
	// to top up.
	Declined
)

type ChargeResult struct {
	Status ChargeStatus
	// The balance after the charge, if you know it. Saves Relume a
	// credits.get call.
	Balance *int
}

// Reversal reasons. New reasons may appear.
const (
	ReasonWorkFailed    = "work_failed"
	ReasonChargeUnknown = "charge_unknown"
	ReasonStaffRefund   = "staff_refund"
)

type Reversal struct {
	UserID string
	// Idempotency key. Credit at most once per ReversalID.
	ReversalID string
	// What to credit back to the wallet. At least 1.
	PartnerCredits int
	// The RelumeCredits being given back, for your records only. Never
	// credit it to the wallet.
	RelumeCredits int
	// The charge being reversed; "" for a staff refund. If you never debited
	// this charge (it timed out before reaching your wallet), credit nothing,
	// and record it as declined: if the charge itself arrives later, it must
	// not debit.
	ChargeID string
	// ReasonWorkFailed, ReasonChargeUnknown, ReasonStaffRefund, or a new one.
	Reason string
}

type ReversalResult struct {
	Balance *int
}

// InvalidCallbackResultError is reported to OnHandlerError when a callback
// returns something the protocol doesn't allow.
type InvalidCallbackResultError struct {
	Message string
}

func (e *InvalidCallbackResultError) Error() string { return "relume: " + e.Message }

type incomingCall struct {
	callType CallType
	userID   string
	charge   Charge
	reversal Reversal
}

type callRefusal struct {
	code    string
	message string
}

// decodeCallBody parses JSON with numbers kept exact, refusing trailing data.
func decodeCallBody(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

// optionalString: a missing field or null is unset; anything else must be a
// string of 1 to max characters.
func optionalString(body map[string]any, field string, max int) (string, bool) {
	value, present := body[field]
	if !present || value == nil {
		return "", true
	}
	return isString(value, 1, max)
}

// optionalStrings: a missing field or null is unset; anything else must be a
// list of 1 to maxItems strings, each of 1 to max characters.
func optionalStrings(body map[string]any, field string, max, maxItems int) ([]string, bool) {
	value, present := body[field]
	if !present || value == nil {
		return nil, true
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 || len(items) > maxItems {
		return nil, false
	}
	strs := make([]string, len(items))
	for i, item := range items {
		if strs[i], ok = isString(item, 1, max); !ok {
			return nil, false
		}
	}
	return strs, true
}

// parseCall reads a verified body. environment is the secret key's: a call
// for the other one is refused.
func parseCall(raw []byte, environment Environment) (incomingCall, *callRefusal) {
	body, ok := decodeCallBody(raw)
	if !ok {
		return incomingCall{}, &callRefusal{"invalid_request", "The body must be a JSON object."}
	}
	callType, _ := body["type"].(string)
	switch CallType(callType) {
	case CallUserGet, CallCreditsGet, CallCreditsCharge, CallCreditsReverse:
	default:
		shown, _ := json.Marshal(body["type"])
		return incomingCall{}, &callRefusal{"unsupported_call_type", fmt.Sprintf("Unsupported call type: %s.", truncate(string(shown), 64))}
	}
	call := incomingCall{callType: CallType(callType)}
	invalid := func(field string) (incomingCall, *callRefusal) {
		return incomingCall{}, &callRefusal{"invalid_request", fmt.Sprintf("Invalid %s for %s.", field, callType)}
	}

	// A call from a Relume deploy older than the field has no environment;
	// that is nothing to check, until a later major release requires it. A
	// value Relume never sends is refused.
	if value, present := body["environment"]; present {
		env, _ := value.(string)
		if env != string(EnvironmentTest) && env != string(EnvironmentLive) {
			return invalid("environment")
		}
		if Environment(env) != environment {
			return incomingCall{}, &callRefusal{"environment_mismatch", fmt.Sprintf("This %s is for %s, but this server's secret key is for %s.", callType, env, environment)}
		}
	}

	userID, ok := isString(body["userId"], 1, 128)
	if !ok {
		return invalid("userId")
	}
	call.userID = userID

	switch call.callType {
	case CallCreditsCharge:
		charge := Charge{UserID: userID}
		if charge.ChargeID, ok = isString(body["chargeId"], 1, 64); !ok {
			return invalid("chargeId")
		}
		if charge.PartnerCredits, ok = isWhole(body["partnerCredits"], 1); !ok {
			return invalid("partnerCredits")
		}
		if charge.RelumeCredits, ok = isWhole(body["relumeCredits"], 0); !ok {
			return invalid("relumeCredits")
		}
		if charge.Action, ok = isString(body["action"], 1, 200); !ok {
			return invalid("action")
		}
		if charge.Description, ok = isString(body["description"], 0, 200); !ok {
			return invalid("description")
		}
		if charge.SiteID, ok = optionalString(body, "siteId", 200); !ok {
			return invalid("siteId")
		}
		if charge.PageID, ok = optionalString(body, "pageId", 200); !ok {
			return invalid("pageId")
		}
		if charge.PageIDs, ok = optionalStrings(body, "pageIds", 200, 1000); !ok {
			return invalid("pageIds")
		}
		call.charge = charge

	case CallCreditsReverse:
		reversal := Reversal{UserID: userID}
		if reversal.ReversalID, ok = isString(body["reversalId"], 1, 64); !ok {
			return invalid("reversalId")
		}
		if reversal.PartnerCredits, ok = isWhole(body["partnerCredits"], 1); !ok {
			return invalid("partnerCredits")
		}
		if reversal.RelumeCredits, ok = isWhole(body["relumeCredits"], 0); !ok {
			return invalid("relumeCredits")
		}
		if reversal.Reason, ok = isString(body["reason"], 1, 200); !ok {
			return invalid("reason")
		}
		if reversal.ChargeID, ok = optionalString(body, "chargeId", 64); !ok {
			return invalid("chargeId")
		}
		call.reversal = reversal
	}
	return call, nil
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// balanceBody is the { balance? } every credits answer carries.
type balanceBody struct {
	Balance *int `json:"balance,omitempty"`
}

func checkBalance(balance int, callback string) error {
	if !isBalance(balance) {
		return &InvalidCallbackResultError{fmt.Sprintf("%s must return a balance that is a whole number, zero or more; got %d.", callback, balance)}
	}
	return nil
}

func checkOptionalBalance(balance *int, callback string) error {
	if balance == nil {
		return nil
	}
	return checkBalance(*balance, callback)
}

func checkUserProfile(profile *UserProfile) error {
	if profile.Name != "" && utf16Len(profile.Name) > 200 {
		return &InvalidCallbackResultError{"OnGetUser: name must be a string of at most 200 characters."}
	}
	if profile.Email != "" && !isEmail(profile.Email) {
		return &InvalidCallbackResultError{"OnGetUser: email must be a valid email address."}
	}
	if profile.AvatarURL != "" && !isHTTPSURL(profile.AvatarURL) {
		return &InvalidCallbackResultError{"OnGetUser: avatarUrl must be an https URL."}
	}
	return nil
}

func callbackFor(options *Options, callType CallType) (string, bool) {
	switch callType {
	case CallUserGet:
		return "OnGetUser", options.OnGetUser != nil
	case CallCreditsGet:
		return "OnGetCredits", options.OnGetCredits != nil
	case CallCreditsCharge:
		return "OnChargeCredits", options.OnChargeCredits != nil
	default:
		return "OnReverseCharge", options.OnReverseCharge != nil
	}
}

var errInternal = errorResponse(500, "internal_error", "The partner failed to handle the call.")

// handleCall answers POST /relume/calls: verifies the signature and the
// 5-minute window, refuses a call for another environment, routes by type,
// runs the callback and checks what it returns.
func (c *Client) handleCall(ctx context.Context, rawBody []byte, signatureHeader, requestID string) response {
	if err := VerifySignature(c.secretKey, signatureHeader, rawBody, c.now()); err != nil {
		message := "The signature doesn't match this server's secret key."
		switch err.(*SignatureError).Reason {
		case SignatureTimestampOutOfTolerance:
			message = "The signature timestamp is more than 5 minutes from this server's clock."
		case SignatureMalformedHeader:
			message = "Missing or malformed Relume-Signature header."
		}
		return errorResponse(401, "invalid_signature", message)
	}

	call, refusal := parseCall(rawBody, c.environment)
	if refusal != nil {
		return errorResponse(400, refusal.code, refusal.message)
	}

	info := HandlerErrorInfo{Route: RouteCalls, Type: call.callType, RequestID: requestID}
	if name, registered := callbackFor(&c.options, call.callType); !registered {
		c.onHandlerError(fmt.Errorf("relume: no %s callback is set, so %s was answered 400", name, call.callType), info)
		return errorResponse(400, "unsupported_call_type", fmt.Sprintf("This partner doesn't handle %s.", call.callType))
	}

	result, err := c.runCallback(ctx, call, CallContext{Type: call.callType, Environment: c.environment, RequestID: requestID})
	if err != nil {
		c.onHandlerError(err, info)
		return errInternal
	}
	return result
}

func (c *Client) runCallback(ctx context.Context, call incomingCall, callContext CallContext) (result response, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("relume: %s callback panicked: %v", call.callType, recovered)
		}
	}()

	switch call.callType {
	case CallUserGet:
		profile, err := c.options.OnGetUser(ctx, call.userID, callContext)
		if err != nil {
			return response{}, err
		}
		if profile == nil {
			return errorResponse(404, "user_not_found", "Unknown user."), nil
		}
		if err := checkUserProfile(profile); err != nil {
			return response{}, err
		}
		return jsonResponse(200, profile), nil

	case CallCreditsGet:
		credits, err := c.options.OnGetCredits(ctx, call.userID, callContext)
		if err != nil {
			return response{}, err
		}
		if err := checkBalance(credits.Balance, "OnGetCredits"); err != nil {
			return response{}, err
		}
		return jsonResponse(200, map[string]int{"balance": credits.Balance}), nil

	case CallCreditsCharge:
		charged, err := c.options.OnChargeCredits(ctx, call.charge, callContext)
		if err != nil {
			return response{}, err
		}
		if err := checkOptionalBalance(charged.Balance, "OnChargeCredits"); err != nil {
			return response{}, err
		}
		switch charged.Status {
		case Charged:
			return jsonResponse(200, balanceBody{Balance: charged.Balance}), nil
		case Declined:
			return jsonResponse(402, struct {
				balanceBody
				Error errorBody `json:"error"`
			}{balanceBody{Balance: charged.Balance}, errorBody{"insufficient_credits", "The user can't afford this action."}}), nil
		default:
			return response{}, &InvalidCallbackResultError{"OnChargeCredits must return a ChargeResult with Status Charged or Declined."}
		}

	default:
		reversed, err := c.options.OnReverseCharge(ctx, call.reversal, callContext)
		if err != nil {
			return response{}, err
		}
		if err := checkOptionalBalance(reversed.Balance, "OnReverseCharge"); err != nil {
			return response{}, err
		}
		return jsonResponse(200, balanceBody{Balance: reversed.Balance}), nil
	}
}

// keyEnvironment is the environment a secret key belongs to, from its prefix.
func keyEnvironment(secretKey string) (Environment, bool) {
	switch {
	case strings.HasPrefix(secretKey, "sk_test_"):
		return EnvironmentTest, true
	case strings.HasPrefix(secretKey, "sk_live_"):
		return EnvironmentLive, true
	}
	return "", false
}
