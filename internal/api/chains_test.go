package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sucopay/sucopay/internal/api"
)

// words is what a deployment's networks and assets have come to, and how many
// times anything asked. A probe reads what the rounds wrote down, so asking
// it is not asking a chain.
type words struct {
	networks map[string]string
	assets   map[string]string
	asked    atomic.Int64
}

func (w *words) Words() (networks, assets map[string]string) {
	w.asked.Add(1)
	return w.networks, w.assets
}

// reading is a deployment reading one network, for the tests that are about
// something else.
func reading() *words {
	return &words{
		networks: map[string]string{"polygon": "observing"},
		assets:   map[string]string{"jpyc": "unchanged"},
	}
}

// probing asks /readyz of a deployment over a reachable database.
func probing(t *testing.T, chains api.Chains) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	api.Handler(quiet(), api.Dependencies{
		Database:    func(context.Context) error { return nil },
		Credentials: inForce{what: "read-write"},
		Chains:      chains,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec
}

// answered is the body of a probe, with the two nested objects read out.
type answered struct {
	Status      string            `json:"status"`
	Database    string            `json:"database"`
	Credentials string            `json:"credentials"`
	Networks    map[string]string `json:"networks"`
	Assets      map[string]string `json:"assets"`
}

func answer(t *testing.T, rec *httptest.ResponseRecorder) answered {
	t.Helper()
	var body answered
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	return body
}

// A probe answers with what the rounds wrote down, read once for the one
// answer. What reads the chains is asked what it has come to, and nothing is
// asked of a chain: a probe is read by whoever can reach the port, and going
// to a provider to answer one would spend this deployment's share of somebody
// else's endpoint on them.
func TestReadyz_SaysWhatEachNetworkAndAssetHasComeTo(t *testing.T) {
	t.Parallel()
	chains := reading()

	rec := probing(t, chains)

	if rec.Code != http.StatusOK {
		t.Errorf("/readyz = %d, want %d", rec.Code, http.StatusOK)
	}
	body := answer(t, rec)
	if body.Networks["polygon"] != "observing" {
		t.Errorf("networks = %v, want polygon observing", body.Networks)
	}
	if body.Assets["jpyc"] != "unchanged" {
		t.Errorf("assets = %v, want jpyc unchanged", body.Assets)
	}
	if asked := chains.asked.Load(); asked != 1 {
		t.Errorf("the probe read what the rounds wrote %d times, want once", asked)
	}
}

// An instance reading no network says nothing about networks, rather than
// answering with an empty object somebody has to work out the meaning of.
func TestReadyz_SaysNothingOfNetworksWhereNothingReadsOne(t *testing.T) {
	t.Parallel()
	rec := probing(t, nil)

	if rec.Code != http.StatusOK {
		t.Errorf("/readyz = %d, want %d", rec.Code, http.StatusOK)
	}
	for _, key := range []string{"networks", "assets"} {
		if strings.Contains(rec.Body.String(), key) {
			t.Errorf("body = %s, want nothing said of %s", rec.Body, key)
		}
	}
}

// Whether a deployment is fit to be sent requests is whether anything is
// reading the chains its payments settle on. The words that mean nobody can
// act take it out; the words that mean somebody has to act leave it in, since
// the API is running and the one who can act is the operator or the provider.
func TestReadyz_IsNotReadyWhenNoNetworkIsBeingRead(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		networks map[string]string
		want     int
	}{
		{"one nobody can reach", map[string]string{"a": "unreachable"}, http.StatusServiceUnavailable},
		{"one whose rounds stopped", map[string]string{"a": "stalled"}, http.StatusServiceUnavailable},
		{"one that is another chain", map[string]string{"a": "chain-mismatch"}, http.StatusServiceUnavailable},
		{"one with no final block", map[string]string{"a": "no-finalized"}, http.StatusServiceUnavailable},
		{"every one of them", map[string]string{"a": "stalled", "b": "unreachable"}, http.StatusServiceUnavailable},
		{"one of two", map[string]string{"a": "stalled", "b": "observing"}, http.StatusOK},
		{"one the chain moved under", map[string]string{"a": "finalized-changed"}, http.StatusOK},
		{"one no round has finished on yet", map[string]string{"a": "no-cursor"}, http.StatusOK},
		{"one whose provider is behind", map[string]string{"a": "finalized-behind"}, http.StatusOK},
		{"none at all", map[string]string{}, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rec := probing(t, &words{networks: c.networks})

			if rec.Code != c.want {
				t.Errorf("/readyz = %d, want %d", rec.Code, c.want)
			}
			want := "ok"
			if c.want != http.StatusOK {
				want = "unavailable"
			}
			if got := answer(t, rec).Status; got != want {
				t.Errorf("status = %q, want %q", got, want)
			}
		})
	}
}

// A network is named in the document, and the endpoint it is read through is
// a secret that may carry a key. Neither the reason a network is not being
// read nor anything of the endpoint is answered with.
func TestReadyz_AnswersWithOneWordPerNetworkAndNothingOfTheEndpoint(t *testing.T) {
	t.Parallel()
	rec := probing(t, &words{networks: map[string]string{"polygon": "unreachable"}})

	body := rec.Body.String()
	for _, secret := range []string{"rpc", "polygon-rpc.example", "key=secret", "http"} {
		if strings.Contains(body, secret) {
			t.Errorf("body = %s, which holds %q", body, secret)
		}
	}
}
