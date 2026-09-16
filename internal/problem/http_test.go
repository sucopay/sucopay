package problem_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/problem"
)

func TestRefuse_LeavesOutTheFieldOfAProblemThatHasNoneAndTheProblemsOfARefusalWithNone(t *testing.T) {
	t.Parallel()
	for want, problems := range map[string][]problem.Field{
		`{"error":"invalid","problems":[{"message":"body is empty"},{"field":"amount","message":"none given"}]}`: {
			{Message: "body is empty"}, {Field: "amount", Message: "none given"},
		},
		`{"error":"invalid"}`: nil,
	} {
		rec := httptest.NewRecorder()

		problem.Refuse(rec, http.StatusBadRequest, "invalid", problems)

		if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("wrote %d %s, want 400 as JSON", rec.Code, rec.Header().Get("Content-Type"))
		}
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Errorf("wrote %s, want %s", got, want)
		}
	}
}

func TestRefuse_QuotesAndCutsWhatTheRequestChose(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("k", problem.MaxFieldBytes+1)
	rec := httptest.NewRecorder()

	problem.Refuse(rec, http.StatusBadRequest, "invalid", []problem.Field{
		{Field: "we\u200bird", Message: "unknown key"}, {Field: long, Message: "unknown key"},
	})

	// The field is quoted so that the character shows, and the quotes are
	// then JSON's to escape.
	body := rec.Body.String()
	if !strings.Contains(body, `"field":"\"we\\u200bird\""`) {
		t.Errorf("body = %s, want the field quoted so that the character shows", body)
	}
	if strings.Contains(body, long) || !strings.Contains(body, "...") {
		t.Errorf("a field of %d bytes was written whole, want it cut to %d", len(long), problem.MaxFieldBytes)
	}
}
