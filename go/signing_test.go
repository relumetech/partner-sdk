package relume

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type vectorFile struct {
	ToleranceSeconds int `json:"toleranceSeconds"`
	Vectors          []struct {
		Name string `json:"name"`
		Sign *struct {
			Secrets       []string `json:"secrets"`
			Timestamp     int64    `json:"timestamp"`
			RawBody       string   `json:"rawBody"`
			SignedPayload string   `json:"signedPayload"`
			Header        string   `json:"header"`
		} `json:"sign"`
		Verify struct {
			Secret   string `json:"secret"`
			Header   string `json:"header"`
			RawBody  string `json:"rawBody"`
			Now      int64  `json:"now"`
			Expected struct {
				Valid  bool   `json:"valid"`
				Reason string `json:"reason"`
			} `json:"expected"`
		} `json:"verify"`
	} `json:"vectors"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestProtocolVectors(t *testing.T) {
	file := loadVectors(t)
	if file.ToleranceSeconds != int(SignatureTolerance/time.Second) || len(file.Vectors) < 16 {
		t.Fatalf("vectors file: tolerance %d, %d vectors", file.ToleranceSeconds, len(file.Vectors))
	}

	for _, vector := range file.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			if sign := vector.Sign; sign != nil {
				header, err := SignCall(sign.Secrets, sign.Timestamp, []byte(sign.RawBody))
				if err != nil {
					t.Fatal(err)
				}
				if header != sign.Header {
					t.Errorf("SignCall = %s, want %s", header, sign.Header)
				}
			}

			verify := vector.Verify
			err := VerifySignature(verify.Secret, verify.Header, []byte(verify.RawBody), time.Unix(verify.Now, 0))
			if verify.Expected.Valid {
				if err != nil {
					t.Errorf("VerifySignature = %v, want valid", err)
				}
			} else {
				var sigErr *SignatureError
				if !errors.As(err, &sigErr) || string(sigErr.Reason) != verify.Expected.Reason {
					t.Errorf("VerifySignature = %v, want %s", err, verify.Expected.Reason)
				}
			}

			// The call handler accepts it only when the vector is valid.
			client, err := New(allCallbacks(Options{SecretKey: verify.Secret, OnHandlerError: func(error, HandlerErrorInfo) {}}))
			if err != nil {
				t.Fatal(err)
			}
			client.now = func() time.Time { return time.Unix(verify.Now, 0) }
			resp := client.handleCall(context.Background(), []byte(verify.RawBody), verify.Header, "")
			want := http.StatusOK
			if !verify.Expected.Valid {
				want = http.StatusUnauthorized
			}
			if resp.status != want {
				t.Errorf("handleCall status %d, want %d", resp.status, want)
			}
		})
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"type":"user.get"}`)
	header, _ := SignCall([]string{testSecret}, testNow.Unix(), body)

	reason := func(err error) SignatureFailure {
		var sigErr *SignatureError
		if !errors.As(err, &sigErr) {
			return ""
		}
		return sigErr.Reason
	}

	t.Run("rejects an empty secret instead of skipping the check", func(t *testing.T) {
		if got := reason(VerifySignature("", header, body, testNow)); got != SignatureMismatch {
			t.Errorf("got %q", got)
		}
	})
	t.Run("rejects a missing header as malformed", func(t *testing.T) {
		if got := reason(VerifySignature(testSecret, "", body, testNow)); got != SignatureMalformedHeader {
			t.Errorf("got %q", got)
		}
	})
	t.Run("never matches a v1 value that is not 64 hex characters", func(t *testing.T) {
		valid := strings.SplitN(header, "v1=", 2)[1]
		for _, signature := range []string{valid[:63], valid + "0", strings.Repeat("z", 64)} {
			got := reason(VerifySignature(testSecret, "t=1793059200,v1="+signature, body, testNow))
			if got != SignatureMismatch {
				t.Errorf("%s: got %q", signature, got)
			}
		}
	})
	t.Run("accepts upper-case hex", func(t *testing.T) {
		upper := "t=1793059200,v1=" + strings.ToUpper(strings.SplitN(header, "v1=", 2)[1])
		if err := VerifySignature(testSecret, upper, body, testNow); err != nil {
			t.Errorf("got %v", err)
		}
	})
	t.Run("checks the signature before the timestamp", func(t *testing.T) {
		late := testNow.Add(time.Hour)
		if got := reason(VerifySignature("sk_test_other", header, body, late)); got != SignatureMismatch {
			t.Errorf("wrong key, stale: got %q", got)
		}
		if got := reason(VerifySignature(testSecret, header, body, late)); got != SignatureTimestampOutOfTolerance {
			t.Errorf("right key, stale: got %q", got)
		}
	})
	t.Run("parses entries with whitespace and ignores unknown names", func(t *testing.T) {
		timestamp, signatures, ok := ParseSignatureHeader(" t = 5 , v2=abc, v1= aa ,junk, v1=bb")
		if !ok || timestamp != 5 || len(signatures) != 2 || signatures[0] != "aa" || signatures[1] != "bb" {
			t.Errorf("got %d %v %v", timestamp, signatures, ok)
		}
	})
}
