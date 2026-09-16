package webhook_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/webhook"
)

func TestNewSecret_IsWhatStandardWebhooksWrites(t *testing.T) {
	t.Parallel()
	s := webhook.NewSecret()

	key, err := s.Key()

	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(s), "whsec_") || len(key) != 32 {
		t.Errorf("secret = %d bytes under %q, want 32 under whsec_", len(key), string(s)[:6])
	}
	if webhook.NewSecret() == s {
		t.Error("two secrets are the same")
	}
}

func TestSecret_ShowsNothingToFmtOrSlog(t *testing.T) {
	t.Parallel()
	s := webhook.NewSecret()
	var out bytes.Buffer
	slog.New(slog.NewJSONHandler(&out, nil)).Info("made", "secret", s)

	for what, rendered := range map[string]string{
		"%v":   fmt.Sprintf("%v", s),
		"%s":   fmt.Sprintf("%s", s),
		"%q":   fmt.Sprintf("%q", s),
		"%#v":  fmt.Sprintf("%#v", s),
		"slog": out.String(),
	} {
		if strings.Contains(rendered, string(s)[6:]) {
			t.Errorf("%s shows the secret: %s", what, rendered)
		}
	}
}

func TestCipher_OpensWhatItSealedAndNothingElse(t *testing.T) {
	t.Parallel()
	one, err := webhook.NewCipher([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	other, err := webhook.NewCipher([32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	s := webhook.NewSecret()

	sealed := one.Seal(s)

	if back, err := one.Open(sealed); err != nil || back != s {
		t.Errorf("Open = %q, %v; want the secret back", back, err)
	}
	if _, err := other.Open(sealed); err == nil {
		t.Error("another key opened the secret")
	}
	if bytes.Equal(one.Seal(s), sealed) {
		t.Error("sealing twice gave the same bytes, which is a nonce used twice")
	}
	if bytes.Contains(sealed, []byte(s)) {
		t.Error("the sealed form carries the secret in the clear")
	}
}
