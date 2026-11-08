package relume

// Relume-Signature verification (Relume Partner Protocol v1, section 6).
//
//	Relume-Signature: t=<unix seconds>,v1=<hex hmac-sha256(sk, t + "." + rawBody)>[,v1=…]
//
// Mirrors Relume's backend signer (services/partners/signing.ts) and passes
// the protocol's test vectors (testdata/). Never log a secret or a signature.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	SignatureHeader = "Relume-Signature"
	RequestIDHeader = "Relume-Request-Id"
	// SignatureTolerance is how far a call's timestamp may be from this
	// server's clock, either way.
	SignatureTolerance = 300 * time.Second
	// MaxSignatures is the current key plus the previous one during a
	// rotation overlap.
	MaxSignatures = 2

	signatureScheme = "v1"
)

var (
	hexSHA256 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	// Canonical decimal: other implementations HMAC the t= text as written,
	// so a leading zero would verify here and fail there.
	unixSeconds = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})$`)
)

// SignatureFailure is why VerifySignature refused a call.
type SignatureFailure string

const (
	SignatureMalformedHeader         SignatureFailure = "malformed_header"
	SignatureMismatch                SignatureFailure = "signature_mismatch"
	SignatureTimestampOutOfTolerance SignatureFailure = "timestamp_out_of_tolerance"
)

// SignatureError is returned by VerifySignature.
type SignatureError struct {
	Reason SignatureFailure
}

func (e *SignatureError) Error() string {
	return "relume: signature refused: " + string(e.Reason)
}

func mac(secret string, timestamp int64, rawBody []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
	h.Write(rawBody)
	return h.Sum(nil)
}

// ComputeSignature is the lowercase hex HMAC-SHA256 of t + "." + rawBody,
// keyed by the secret key.
func ComputeSignature(secret string, timestamp int64, rawBody []byte) (string, error) {
	if secret == "" {
		return "", errors.New("relume: secret must be a non-empty string")
	}
	if timestamp < 0 {
		return "", errors.New("relume: timestamp must be whole unix seconds")
	}
	return hex.EncodeToString(mac(secret, timestamp, rawBody)), nil
}

// SignCall builds a Relume-Signature header the way Relume does: newest
// secret first, a second one only during a rotation. For a partner's own
// tests; a partner's server never needs to sign anything.
func SignCall(secrets []string, timestamp int64, rawBody []byte) (string, error) {
	if len(secrets) == 0 || len(secrets) > MaxSignatures {
		return "", fmt.Errorf("relume: expected 1 to %d secrets, got %d", MaxSignatures, len(secrets))
	}
	parts := []string{"t=" + strconv.FormatInt(timestamp, 10)}
	for _, secret := range secrets {
		signature, err := ComputeSignature(secret, timestamp, rawBody)
		if err != nil {
			return "", err
		}
		parts = append(parts, signatureScheme+"="+signature)
	}
	return strings.Join(parts, ","), nil
}

// ParseSignatureHeader returns the header's timestamp and every v1= value in
// order, including ones that aren't hex. ok is false when the header has no
// t, more than one t, a t that isn't canonical unix seconds, no v1= value, or
// more than two. Entries with another name (a future v2=) are ignored.
func ParseSignatureHeader(header string) (timestamp int64, signatures []string, ok bool) {
	haveTimestamp := false
	for _, part := range strings.Split(header, ",") {
		name, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		switch name {
		case "t":
			if haveTimestamp || !unixSeconds.MatchString(value) {
				return 0, nil, false
			}
			timestamp, _ = strconv.ParseInt(value, 10, 64)
			haveTimestamp = true
		case signatureScheme:
			if len(signatures) == MaxSignatures {
				return 0, nil, false
			}
			signatures = append(signatures, value)
		}
	}
	if !haveTimestamp || len(signatures) == 0 {
		return 0, nil, false
	}
	return timestamp, signatures, true
}

// VerifySignature checks a call: the header parses, any v1= value matches
// the HMAC under secret (compared in constant time), and t is within
// SignatureTolerance of now. The signature is checked before the timestamp,
// so a stale result is only reported for a call Relume really signed.
// Returns a *SignatureError, or nil.
func VerifySignature(secret, header string, rawBody []byte, now time.Time) error {
	timestamp, signatures, ok := ParseSignatureHeader(header)
	if !ok {
		return &SignatureError{Reason: SignatureMalformedHeader}
	}
	// A blank key (an unset env var) rejects the call; it never skips the check.
	if secret == "" {
		return &SignatureError{Reason: SignatureMismatch}
	}

	expected := mac(secret, timestamp, rawBody)
	matched := false
	// Check every value, so the time taken doesn't depend on which one matched.
	for _, signature := range signatures {
		if !hexSHA256.MatchString(signature) {
			continue
		}
		given, _ := hex.DecodeString(signature)
		if hmac.Equal(given, expected) {
			matched = true
		}
	}
	if !matched {
		return &SignatureError{Reason: SignatureMismatch}
	}

	skew := now.Unix() - timestamp
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(SignatureTolerance/time.Second) {
		return &SignatureError{Reason: SignatureTimestampOutOfTolerance}
	}
	return nil
}
