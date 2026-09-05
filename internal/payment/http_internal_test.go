package payment

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
)

// listed is what a document lists, for these tests: assets by the name a
// request writes.
type listed map[string]Asset

func (l listed) Asset(name string) (Asset, bool) {
	a, ok := l[name]
	return a, ok
}

// theList lists one asset with 18 decimals, as a document lists JPYC.
func theList(t *testing.T) listed {
	t.Helper()
	jpyc, err := NewAsset("polygon", "0x431d5dff03120afa4bdf332c61a6e1766ef37bdb", "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	return listed{"jpyc": jpyc}
}

// example is a body that writes every key a body may hold.
const example = `{
  "asset": "jpyc",
  "amount": "1000",
  "expires_at": "2026-09-05T13:00:00Z",
  "metadata": {"order": "A-1"}
}`

// fields joins the fields of problems with a space, in the order they were
// reported, writing "-" for a problem that has none.
func fields(problems Problems) string {
	var out []string
	for _, p := range problems {
		if p.Field == "" {
			out = append(out, "-")
			continue
		}
		out = append(out, p.Field)
	}
	return strings.Join(out, " ")
}

func TestReadRequest_ReadsABodyThatWritesEveryKey(t *testing.T) {
	t.Parallel()
	r, problems := readRequest([]byte(example), theList(t))
	if problems != nil {
		t.Fatal(problems)
	}

	if r.asset.Symbol() != "JPYC" || r.amount.Units() != "1000" || !r.amount.Asset().Same(r.asset) {
		t.Errorf("read %s of %s, want 1000 JPYC", r.amount, r.asset)
	}
	if want := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC); !r.expiresAt.Equal(want) {
		t.Errorf("expires at %s, want %s", r.expiresAt, want)
	}
	if len(r.metadata) != 1 || r.metadata["order"] != "A-1" {
		t.Errorf("metadata = %v, want order A-1", r.metadata)
	}
}

func TestReadRequest_LeavesOutWhatTheBodyLeavesOut(t *testing.T) {
	t.Parallel()
	r, problems := readRequest([]byte(`{"asset": "jpyc", "amount": "1"}`), theList(t))
	if problems != nil {
		t.Fatal(problems)
	}

	if !r.expiresAt.IsZero() {
		t.Errorf("expires at %s, want the zero time for a body naming none", r.expiresAt)
	}
	if r.metadata != nil {
		t.Errorf("metadata = %v, want none", r.metadata)
	}
}

func TestReadRequest_ReportsEveryProblemUnderItsField(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body string
		// fields is what fields reports, which fixes the order too: two
		// readings of one body report its problems in one order.
		fields string
		// said is a word one of the messages has to hold.
		said string
	}{
		{"keys the API has no use for", `{"asset": "jpyc", "amount": "1", "destination": "0x1", "id": "p", "account": "a"}`,
			"account destination id", "unknown key"},
		{"a key of no characters", `{"asset": "jpyc", "amount": "1", "": "x"}`, `""`, "unknown key"},
		{"an amount written as a number", `{"asset": "jpyc", "amount": 1000}`, "amount", "want a string, got a number"},
		{"an asset written as a list", `{"asset": ["jpyc"], "amount": "1"}`, "asset", "want a string, got an array"},
		{"null for a key", `{"asset": null, "amount": "1"}`, "asset", "want a string, got null"},
		{"an asset nobody lists and a signed amount", `{"asset": "gold", "amount": "-1"}`, "asset amount", "digits"},
		{"an asset nobody lists", `{"asset": "gold", "amount": "1"}`, "asset", "gold"},
		{"nothing", `{}`, "asset amount", "none given"},
		{"a metadata value that is a number", `{"asset": "jpyc", "amount": "1", "metadata": {"order": 1}}`, "metadata", "got a number under order"},
		{"a metadata value that is null", `{"asset": "jpyc", "amount": "1", "metadata": {"order": null}}`, "metadata", "got null under order"},
		{"a metadata value that is true", `{"asset": "jpyc", "amount": "1", "metadata": {"order": true}}`, "metadata", "got a boolean under order"},
		{"metadata that is a list", `{"asset": "jpyc", "amount": "1", "metadata": ["a"]}`, "metadata", "want an object of strings, got an array"},
		{"metadata that is null", `{"asset": "jpyc", "amount": "1", "metadata": null}`, "metadata", "want an object of strings, got null"},
		{"an expiry that is not a time", `{"asset": "jpyc", "amount": "1", "expires_at": "tomorrow"}`, "expires_at", "RFC 3339"},
		{"an expiry with no zone", `{"asset": "jpyc", "amount": "1", "expires_at": "2026-09-05T13:00:00"}`, "expires_at", "RFC 3339"},
		{"an expiry at the zero time", `{"asset": "jpyc", "amount": "1", "expires_at": "0001-01-01T00:00:00Z"}`, "expires_at", "not after now"},
		{"an expiry at the zero time in another zone", `{"asset": "jpyc", "amount": "1", "expires_at": "0001-01-01T09:00:00+09:00"}`, "expires_at", "not after now"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, problems := readRequest([]byte(c.body), theList(t))

			if got := fields(problems); got != c.fields {
				t.Errorf("problems at %q, want %q: %v", got, c.fields, problems)
			}
			if !strings.Contains(problems.Error(), c.said) {
				t.Errorf("no problem says %q: %v", c.said, problems)
			}
			if r.asset.IsSet() || r.amount.IsSet() || !r.expiresAt.IsZero() || r.metadata != nil {
				t.Errorf("a refused body was read as %+v", r)
			}
		})
	}
}

func TestReadRequest_CountsPlacesOnlyAgainstAnAssetItHas(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, body, fields string }{
		{"a fraction of an asset nobody lists", `{"asset": "gold", "amount": "1.5"}`, "asset"},
		{"a long amount of an asset nobody lists", `{"asset": "gold", "amount": "` + strings.Repeat("9", MaxAmountDigits) + `"}`, "asset"},
		{"more places than the asset has", `{"asset": "jpyc", "amount": "1.` + strings.Repeat("0", 18) + `1"}`, "amount"},
		{"as many places as the asset has", `{"asset": "jpyc", "amount": "1.` + strings.Repeat("0", 17) + `1"}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, problems := readRequest([]byte(c.body), theList(t))

			if got := fields(problems); got != c.fields {
				t.Errorf("problems at %q, want %q: %v", got, c.fields, problems)
			}
		})
	}
}

func TestReadRequest_RefusesABodyThatIsNotOneJSONObject(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, body, said string }{
		{"nothing", ``, "empty"},
		{"null", `null`, "null"},
		{"a list", `[]`, "array"},
		{"a string", `"jpyc"`, "string"},
		{"text", `asset=jpyc`, "not JSON"},
		{"an object cut short", `{"asset": "jpyc"`, "ends"},
		{"an object with more after it", `{"asset": "jpyc", "amount": "1"} x`, "after"},
		{"two objects", `{"asset": "jpyc", "amount": "1"} {}`, "after"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, problems := readRequest([]byte(c.body), theList(t))

			if len(problems) != 1 || problems[0].Field != "" {
				t.Fatalf("problems = %v, want one with no field", problems)
			}
			if !strings.Contains(problems[0].Message, c.said) {
				t.Errorf("the problem says %q, want it to say %q", problems[0].Message, c.said)
			}
		})
	}
}

func TestReadRequest_QuotesWhatItRepeatsOfTheBody(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, value, quoted string }{
		{"a name with a character a reader cannot see", "je\u200bwel", `"je\u200bwel"`},
		{"a name of no characters", "", `""`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, problems := readRequest([]byte(`{"asset": `+strconv.Quote(c.value)+`, "amount": "1"}`), theList(t))

			if len(problems) != 1 || invisible.Has(problems[0].Message) {
				t.Fatalf("problems = %q, want one that a terminal shows whole", problems)
			}
			if !strings.Contains(problems[0].Message, c.quoted+" is not") {
				t.Errorf("the problem says %q, want the name written %s", problems[0].Message, c.quoted)
			}
		})
	}
}

func TestProblemsJSON_LeavesOutTheFieldOfAProblemThatHasNone(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(errorJSON{Error: "invalid", Problems: problemsJSON(Problems{
		{Message: "body is empty"}, {Field: "amount", Message: "none given"},
	})})
	if err != nil {
		t.Fatal(err)
	}

	want := `{"error":"invalid","problems":[{"message":"body is empty"},{"field":"amount","message":"none given"}]}`
	if string(body) != want {
		t.Errorf("wrote %s, want %s", body, want)
	}
}

func TestProblemsJSON_LeavesOutTheProblemsOfAnErrorThatHasNone(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(errorJSON{Error: "not_found"})
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"error":"not_found"}`; string(body) != want {
		t.Errorf("wrote %s, want %s", body, want)
	}
}

func TestProblemsJSON_QuotesAndCutsWhatTheRequestChose(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("k", maxProblemBytes+1)
	out := problemsJSON(Problems{{Field: "we\u200bird", Message: "unknown key"}, {Field: long, Message: "unknown key"}})

	if out[0].Field != strconv.Quote("we\u200bird") {
		t.Errorf("field = %q, want it quoted so that the character shows", out[0].Field)
	}
	if len(out[1].Field) > maxProblemBytes+len("...") || !strings.HasSuffix(out[1].Field, "...") {
		t.Errorf("a field of %d bytes came out as %d, want it cut to %d", len(long), len(out[1].Field), maxProblemBytes)
	}
}

// FuzzReadRequest reads whatever a client sends, and checks what came back
// against the body as read by encoding/json on its own.
func FuzzReadRequest(f *testing.F) {
	for _, seed := range []string{
		example,
		`{"asset": "jpyc", "amount": 1000}`,
		`{"asset": "jpyc", "amount": "0"}`,
		`{"asset": "jpyc", "amount": "-1"}`,
		`{"asset": "jpyc", "amount": "1e3"}`,
		`{"asset": "jpyc", "amount": "+1"}`,
		`{"asset": "jpyc", "amount": "1", "destination": "0x1"}`,
		`{"asset": "jpyc", "amount": "1", "metadata": {"a": "b"}, "expires_at": "2026-01-01T00:00:00+09:00"}`,
		`{"asset": "gold", "amount": "1.5"}`,
		`{"asset": "je\u200bwel", "amount": "1", "metadata": {"or\u200bder": 1}}`,
		`{"asset": "jpyc", "amount": "1", "amount asset": "x", "": ""}`,
		``, `{}`, `[]`, `null`, `{"asset": "jpyc"} x`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		r, problems := readRequest([]byte(body), theList(t))

		// What encoding/json makes of the body on its own. Unmarshal refuses
		// what comes after a value, which readRequest must refuse too.
		var keys map[string]json.RawMessage
		unreadable := json.Unmarshal([]byte(body), &keys) != nil || keys == nil
		switch {
		case unreadable && (len(problems) != 1 || problems[0].Field != ""):
			t.Fatalf("readRequest(%q) reported %v for a body that is not one object, want one problem with no field", body, problems)
		case unreadable:
			return
		}
		// field is what a problem about key is reported under.
		field := func(key string) string {
			if key == "" {
				return `""`
			}
			return key
		}
		reported := map[string]bool{}
		for _, p := range problems {
			if p.Message == "" {
				t.Errorf("readRequest(%q) reported a problem with no message", body)
			}
			if p.Field == "" {
				t.Errorf("readRequest(%q) reported %q with no field for a body that is an object", body, p.Message)
			}
			// A message may repeat a value of the body; a field is a key of it,
			// and problemsJSON quotes it.
			if invisible.Has(p.Message) {
				t.Errorf("readRequest(%q) reported %q, which holds a character a reader cannot see", body, p.Message)
			}
			reported[p.Field] = true
		}
		known := []string{"amount", "asset", "expires_at", "metadata"}
		for key := range keys {
			if !slices.Contains(known, key) && !reported[field(key)] {
				t.Errorf("readRequest(%q) let the key %q through", body, key)
			}
		}
		for _, key := range []string{"asset", "amount"} {
			if _, ok := keys[key]; !ok && !reported[key] {
				t.Errorf("readRequest(%q) read a body with no %s", body, key)
			}
		}
		for f := range reported {
			_, ofTheBody := keys[f]
			_, ofTheEmptyKey := keys[""]
			if !ofTheBody && (f != `""` || !ofTheEmptyKey) && f != "asset" && f != "amount" {
				t.Errorf("readRequest(%q) reported a problem at %q, which the body has no key of", body, f)
			}
		}
		if len(problems) > 0 {
			if r.asset.IsSet() || r.amount.IsSet() {
				t.Errorf("readRequest(%q) reported %v and read a request too", body, problems)
			}
			return
		}
		if !r.asset.IsSet() || !r.amount.IsSet() || !r.amount.Asset().Same(r.asset) {
			t.Fatalf("readRequest(%q) reported no problem and read %+v", body, r)
		}
		// The amount, written back in the asset's units and read once more,
		// is the same count of the smallest unit.
		again, err := ParseUnits(r.asset, r.amount.Units())
		if err != nil {
			t.Fatalf("readRequest(%q) read %s, which does not read back: %v", body, r.amount, err)
		}
		if cmp, err := again.Cmp(r.amount); err != nil || cmp != 0 {
			t.Errorf("readRequest(%q) read %s, which reads back as %s", body, r.amount, again)
		}
	})
}
