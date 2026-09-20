package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	paused   map[string]bool
	asked    atomic.Int64
}

func (w *words) Words() (networks, assets map[string]string) {
	w.asked.Add(1)
	return w.networks, w.assets
}

func (w *words) Paused() map[string]bool { return w.paused }

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
	return probingSettled(t, chains, nil)
}

// probingSettled is the same, of a deployment that also settles what it read.
func probingSettled(t *testing.T, chains api.Chains, decides api.Settling) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	api.Handler(quiet(), api.Dependencies{
		Database:    func(context.Context) error { return nil },
		Credentials: inForce{what: "read-write"},
		Chains:      chains,
		Settling:    decides,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec
}

// settles is a deployment settling what it recorded, in the word apiece a
// probe answers with.
type settles map[string]string

func (s settles) Words() map[string]string { return s }

// answered is the body of a probe, with the two nested objects read out.
type answered struct {
	Status      string            `json:"status"`
	Database    string            `json:"database"`
	Credentials string            `json:"credentials"`
	Networks    map[string]string `json:"networks"`
	Assets      map[string]string `json:"assets"`
	Paused      map[string]bool   `json:"paused"`
	Finality    map[string]string `json:"finality"`
	Webhooks    string            `json:"webhooks"`
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
	for _, key := range []string{"networks", "assets", "paused"} {
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

// Reading a network and settling what was read are two things, and a probe
// answers for both. One can be going while the other is not.
func TestReadyz_SaysWhatEachNetworksSettlingHasComeTo(t *testing.T) {
	t.Parallel()

	rec := probingSettled(t, reading(), settles{"polygon": "deciding"})

	if rec.Code != http.StatusOK {
		t.Errorf("/readyz = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := answer(t, rec); body.Finality["polygon"] != "deciding" {
		t.Errorf("finality = %v, want polygon deciding", body.Finality)
	}
}

// Said and not acted on. A deployment that settles nothing still sees payments
// arrive and still records them, and the funds are at the merchant's address
// either way; what is stuck is the judgement. Taking the API out of service
// over it would stop the payments that are still being made.
func TestReadyz_IsReadyWhileNothingIsBeingSettled(t *testing.T) {
	t.Parallel()

	rec := probingSettled(t, reading(), settles{"polygon": "stalled"})

	if rec.Code != http.StatusOK {
		t.Errorf("/readyz = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := answer(t, rec); body.Finality["polygon"] != "stalled" {
		t.Errorf("finality = %v, want polygon stalled", body.Finality)
	}
}

// An instance settling nothing says nothing about it, rather than answering
// with an empty object somebody has to work out the meaning of.
func TestReadyz_SaysNothingOfSettlingWhereNothingSettles(t *testing.T) {
	t.Parallel()

	rec := probingSettled(t, reading(), nil)

	if body := answer(t, rec); body.Finality != nil {
		t.Errorf("finality = %v, want nothing", body.Finality)
	}
}

// delivers is a deployment delivering webhooks, in the one word a probe
// answers with.
type delivers string

func (d delivers) Word() string { return string(d) }

// Whether webhooks are being delivered is a third thing a probe says of a
// deployment, and nothing where nothing is delivered.
func TestReadyz_SaysWhatTheWebhookDeliveringHasComeToAndNothingWhereThereIsNone(t *testing.T) {
	t.Parallel()
	probe := func(d api.Delivering) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.Handler(quiet(), api.Dependencies{
			Database:    func(context.Context) error { return nil },
			Credentials: inForce{what: "read-write"},
			Chains:      reading(),
			Delivering:  d,
		}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec
	}

	if body := answer(t, probe(delivers("delivering"))); body.Webhooks != "delivering" || body.Status != "ok" {
		t.Errorf("webhooks = %q, status %q; want delivering and ok", body.Webhooks, body.Status)
	}
	if body := answer(t, probe(delivers("stalled"))); body.Webhooks != "stalled" || body.Status != "ok" {
		t.Errorf("webhooks = %q, status %q; want stalled said and the instance still ok", body.Webhooks, body.Status)
	}
	if rec := probe(nil); strings.Contains(rec.Body.String(), "webhooks") {
		t.Errorf("body = %s, want no webhooks where nothing delivers", rec.Body)
	}
}

// An issuer stopping a token is a fact about the token, and one the
// deployment can do nothing about: the API is up, payments are recorded, and
// the funds that arrive are the merchant's. So the probe says which assets
// are paused, and the deployment stays in service while it does.
func TestReady_SaysWhichAssetsArePausedAndStaysInServiceWhileOneIs(t *testing.T) {
	t.Parallel()
	chains := reading()
	chains.paused = map[string]bool{"jpyc": true, "other": false}

	rec := probing(t, chains)

	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200: a paused token does not take a deployment out", rec.Code)
	}
	body := answer(t, rec)
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if want := map[string]bool{"jpyc": true, "other": false}; !reflect.DeepEqual(body.Paused, want) {
		t.Errorf("paused = %v, want %v", body.Paused, want)
	}
}

// An asset nothing has read lately is left out, and a deployment where that
// is every asset has no field at all: a field a reader finds is one with
// something in it.
func TestReady_LeavesPausedOutWhereNoAssetWasRead(t *testing.T) {
	t.Parallel()
	rec := probing(t, reading())

	if strings.Contains(rec.Body.String(), "paused") {
		t.Errorf("the body carries a paused field with nothing to say:\n%s", rec.Body)
	}
}
