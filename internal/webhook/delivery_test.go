package webhook_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/webhook"
)

func TestEnvelope_WrapsTheDataUnderTypeTimestampAndAccount(t *testing.T) {
	t.Parallel()
	occurred := time.Date(2026, 9, 16, 1, 23, 45, 0, time.FixedZone("JST", 9*3600))

	body, err := webhook.Envelope("payment.succeeded", occurred, first, json.RawMessage(`{"id":"p1","note":"a<b"}`))

	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"payment.succeeded","timestamp":"2026-09-15T16:23:45Z","account":"` + string(first) + `","data":{"id":"p1","note":"a<b"}}`
	if string(body) != want {
		t.Errorf("body = %s\nwant   %s", body, want)
	}
	if _, err := webhook.Envelope("endpoint.test", occurred, first, json.RawMessage(`{`)); err == nil {
		t.Error("data that is not JSON was wrapped")
	}
}

func TestNextAttempt_FollowsTheScheduleAndStopsAtTheTenth(t *testing.T) {
	t.Parallel()
	none := func() float64 { return 0 }
	most := func() float64 { return 0.999 }
	want := []time.Duration{5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour,
		5 * time.Hour, 10 * time.Hour, 14 * time.Hour, 20 * time.Hour, 24 * time.Hour}

	for failed, wait := range want {
		at, again := webhook.NextAttempt(failed+1, now, none)
		if !again || at.Sub(now) != wait {
			t.Errorf("after attempt %d: %v, %t; want %v", failed+1, at.Sub(now), again, wait)
		}
		late, _ := webhook.NextAttempt(failed+1, now, most)
		if extra := late.Sub(at); extra < 0 || extra > wait/10 {
			t.Errorf("after attempt %d the jitter added %v, want at most a tenth of %v", failed+1, extra, wait)
		}
	}
	if _, again := webhook.NextAttempt(webhook.MaxAttempts, now, none); again {
		t.Errorf("attempt %d failing was given another", webhook.MaxAttempts)
	}
	if _, again := webhook.NextAttempt(0, now, none); again {
		t.Error("an attempt that never happened was given another")
	}
}
