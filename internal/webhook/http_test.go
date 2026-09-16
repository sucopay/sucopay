package webhook_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/webhook"
)

// served is an HTTP over a fresh store, resolving every name to one public
// address.
func served(t *testing.T) *webhook.HTTP {
	t.Helper()
	s, _ := store(t)
	return webhook.NewHTTP(s, answering{"93.184.216.34"}, func() time.Time { return now })
}

// answered is the response to one call of method for account, with body as
// the request's.
func answered(t *testing.T, method func(http.ResponseWriter, *http.Request, payment.AccountID) error,
	verb, target, body string, account payment.AccountID) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(verb, target, strings.NewReader(body))
	// The id the route hands on; the mux is not here to set it.
	if i := strings.Index(target, "/webhook_endpoints/"); i >= 0 {
		rest := target[i+len("/webhook_endpoints/"):]
		r.SetPathValue("id", strings.SplitN(rest, "/", 2)[0])
	}
	if err := method(rec, r, account); err != nil {
		t.Fatal(err)
	}
	return rec
}

// problems reads the problems of a refusal, by field.
func problems(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body struct {
		Error    string `json:"error"`
		Problems []struct{ Field, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %s", rec.Body)
	}
	if body.Error != "invalid" {
		t.Errorf("error = %q, want invalid", body.Error)
	}
	out := map[string]string{}
	for _, p := range body.Problems {
		out[p.Field] = p.Message
	}
	return out
}

func registered(t *testing.T, h *webhook.HTTP, account payment.AccountID) map[string]any {
	t.Helper()
	rec := answered(t, h.Create, http.MethodPost, "/webhook_endpoints",
		`{"url":"https://hooks.example/in","description":"orders","events":["payment.succeeded","payment.succeeded"]}`, account)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create = %d %s, want 201", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestHTTP_CreateShowsTheSecretOnceAndNoLaterAnswerRepeatsIt(t *testing.T) {
	t.Parallel()
	h := served(t)

	body := registered(t, h, first)

	secret, _ := body["secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") {
		t.Errorf("secret = %q, want one under whsec_", secret)
	}
	if events, _ := body["events"].([]any); len(events) != 1 {
		t.Errorf("events = %v, want the repeat dropped", body["events"])
	}
	id, _ := body["id"].(string)
	for what, rec := range map[string]*httptest.ResponseRecorder{
		"Read": answered(t, h.Read, http.MethodGet, "/webhook_endpoints/"+id, "", first),
		"List": answered(t, h.List, http.MethodGet, "/webhook_endpoints", "", first),
		"Update": answered(t, h.Update, http.MethodPatch, "/webhook_endpoints/"+id,
			`{"description":"orders, renamed"}`, first),
	} {
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d %s, want 200", what, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s repeats the secret", what)
		}
	}
}

func TestHTTP_RefusesWhatARegistrationMayNotCarry(t *testing.T) {
	t.Parallel()
	h := served(t)
	for what, c := range map[string]struct{ body, field string }{
		"no url":              {`{"description":"x"}`, "url"},
		"http":                {`{"url":"http://hooks.example/in"}`, "url"},
		"a username":          {`{"url":"https://u:p@hooks.example/in"}`, "url"},
		"an unknown key":      {`{"url":"https://hooks.example/in","secret":"mine"}`, "secret"},
		"a long description":  {`{"url":"https://hooks.example/in","description":"` + strings.Repeat("x", 201) + `"}`, "description"},
		"an unseen character": {"{\"url\":\"https://hooks.example/in\",\"description\":\"a\u200bb\"}", "description"},
		"an unknown event":    {`{"url":"https://hooks.example/in","events":["payment.paid"]}`, "events[0]"},
		"no event at all":     {`{"url":"https://hooks.example/in","events":[]}`, "events"},
		"events not a list":   {`{"url":"https://hooks.example/in","events":"payment.succeeded"}`, "events"},
		"a long events list":  {`{"url":"https://hooks.example/in","events":[` + strings.Repeat(`"payment.succeeded",`, 32) + `"payment.succeeded"]}`, "events"},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			rec := answered(t, h.Create, http.MethodPost, "/webhook_endpoints", c.body, first)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d %s, want 400", rec.Code, rec.Body)
			}
			if _, named := problems(t, rec)[c.field]; !named {
				t.Errorf("problems = %s, want one naming %s", rec.Body, c.field)
			}
		})
	}
	for what, body := range map[string]string{
		"empty": "", "an array": "[]", "null": "null", "two values": "{} {}", "not JSON": "{",
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			if rec := answered(t, h.Create, http.MethodPost, "/webhook_endpoints", body, first); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHTTP_RefusesADestinationInsideTheDeployment(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	h := webhook.NewHTTP(s, answering{"169.254.169.254"}, func() time.Time { return now })

	rec := answered(t, h.Create, http.MethodPost, "/webhook_endpoints", `{"url":"https://hooks.example/in"}`, first)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %s, want 400", rec.Code, rec.Body)
	}
	if msg := problems(t, rec)["url"]; msg == "" || strings.Contains(msg, "169.254") {
		t.Errorf("url problem = %q, want one that does not repeat the address", msg)
	}
}

func TestHTTP_RefusesANinthEndpointAsInvalid(t *testing.T) {
	t.Parallel()
	h := served(t)
	for range webhook.MaxEndpoints {
		registered(t, h, first)
	}

	rec := answered(t, h.Create, http.MethodPost, "/webhook_endpoints", `{"url":"https://hooks.example/in"}`, first)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "8 endpoints") {
		t.Errorf("status = %d %s, want 400 saying how many an account may hold", rec.Code, rec.Body)
	}
}

func TestHTTP_AnswersAnotherAccountsEndpointAsNoneOnEveryRoute(t *testing.T) {
	t.Parallel()
	h := served(t)
	id, _ := registered(t, h, first)["id"].(string)
	for what, rec := range map[string]*httptest.ResponseRecorder{
		"Read":              answered(t, h.Read, http.MethodGet, "/webhook_endpoints/"+id, "", other),
		"Update":            answered(t, h.Update, http.MethodPatch, "/webhook_endpoints/"+id, `{"enabled":false}`, other),
		"Update of the URL": answered(t, h.Update, http.MethodPatch, "/webhook_endpoints/"+id, `{"url":"http://hooks.example/in"}`, other),
		"Rotate":            answered(t, h.Rotate, http.MethodPost, "/webhook_endpoints/"+id+"/secret", "", other),
		"Delete":            answered(t, h.Delete, http.MethodDelete, "/webhook_endpoints/"+id, "", other),
		"Read of no shape":  answered(t, h.Read, http.MethodGet, "/webhook_endpoints/nope", "", first),
	} {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d %s, want 404", what, rec.Code, rec.Body)
		}
	}
	if rec := answered(t, h.Read, http.MethodGet, "/webhook_endpoints/"+id, "", first); rec.Code != http.StatusOK {
		t.Errorf("the owner's Read = %d, want 200", rec.Code)
	}
}

func TestHTTP_UpdateChangesWhatTheBodyNamesAndChecksANewURL(t *testing.T) {
	t.Parallel()
	h := served(t)
	id, _ := registered(t, h, first)["id"].(string)

	rec := answered(t, h.Update, http.MethodPatch, "/webhook_endpoints/"+id,
		`{"url":"https://hooks.example/v2","events":null,"enabled":false}`, first)

	if rec.Code != http.StatusOK {
		t.Fatalf("Update = %d %s, want 200", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["url"] != "https://hooks.example/v2" || body["events"] != nil || body["enabled"] != false || body["description"] != "orders" {
		t.Errorf("Update = %v, want url, events and enabled changed and description kept", body)
	}
	for what, c := range map[string]struct{ body, field string }{
		"http":             {`{"url":"http://hooks.example/in"}`, "url"},
		"enabled a word":   {`{"enabled":"yes"}`, "enabled"},
		"an unknown key":   {`{"id":"x"}`, "id"},
		"an unknown event": {`{"events":["nothing"]}`, "events[0]"},
	} {
		rec := answered(t, h.Update, http.MethodPatch, "/webhook_endpoints/"+id, c.body, first)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", what, rec.Code)
			continue
		}
		if _, named := problems(t, rec)[c.field]; !named {
			t.Errorf("%s: problems = %s, want one naming %s", what, rec.Body, c.field)
		}
	}
}

func TestHTTP_RotateShowsANewSecretAndDeleteAnswersNoContent(t *testing.T) {
	t.Parallel()
	h := served(t)
	made := registered(t, h, first)
	id, _ := made["id"].(string)

	rec := answered(t, h.Rotate, http.MethodPost, "/webhook_endpoints/"+id+"/secret", "", first)

	if rec.Code != http.StatusOK {
		t.Fatalf("Rotate = %d %s, want 200", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body["secret"], "whsec_") || body["secret"] == made["secret"] {
		t.Errorf("secret = %q, want a new one", body["secret"])
	}
	if rec := answered(t, h.Delete, http.MethodDelete, "/webhook_endpoints/"+id, "", first); rec.Code != http.StatusNoContent {
		t.Errorf("Delete = %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := answered(t, h.List, http.MethodGet, "/webhook_endpoints", "", first); !strings.Contains(rec.Body.String(), `"endpoints":[]`) {
		t.Errorf("List after Delete = %s, want none", rec.Body)
	}
}
