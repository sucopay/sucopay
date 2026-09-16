package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/webhook"
)

// verify is a receiver's side of Standard Webhooks, written from the
// specification rather than from the signer: strip whsec_, decode the
// secret, HMAC-SHA256 "id.timestamp.body", and accept any v1 signature in
// the header that matches.
func verify(secret webhook.Secret, id, timestamp, signature string, body []byte) bool {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(secret), "whsec_"))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "." + string(body)))
	want := mac.Sum(nil)
	for _, part := range strings.Fields(signature) {
		version, sig, found := strings.Cut(part, ",")
		if !found || version != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(sig)
		if err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

func TestSign_VerifiesUnderEachSecretItWasSignedWithAndNoOther(t *testing.T) {
	t.Parallel()
	fresh, old, other := webhook.NewSecret(), webhook.NewSecret(), webhook.NewSecret()
	id := webhook.ID(strings.Repeat("a", 32))
	body := []byte(`{"type":"endpoint.test","data":{}}`)
	timestamp := webhook.Timestamp(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))

	signature, err := webhook.Sign([]webhook.Secret{fresh, old}, id, timestamp, body)

	if err != nil {
		t.Fatal(err)
	}
	if timestamp != "1789560000" {
		t.Errorf("timestamp = %s, want seconds since the epoch", timestamp)
	}
	if !verify(fresh, string(id), timestamp, signature, body) || !verify(old, string(id), timestamp, signature, body) {
		t.Errorf("a receiver holding either secret does not verify %q", signature)
	}
	if verify(other, string(id), timestamp, signature, body) {
		t.Error("a receiver holding another secret verifies")
	}
	if verify(fresh, string(id), timestamp, signature, append(body, ' ')) {
		t.Error("a body changed on the way verifies")
	}
	if verify(fresh, string(id), "1789560001", signature, body) {
		t.Error("a timestamp changed on the way verifies")
	}
	if strings.Count(signature, "v1,") != 2 {
		t.Errorf("signature = %q, want one v1 per secret", signature)
	}
}
