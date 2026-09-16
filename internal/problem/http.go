package problem

import (
	"encoding/json"
	"net/http"

	"github.com/sucopay/sucopay/internal/invisible"
)

// Field is one problem as an HTTP response carries it: which key of the
// request it is about, and what is wrong. A problem with no field is about
// the body as a whole.
type Field struct {
	Field   string
	Message string
}

// fieldJSON is a Field as a response writes it.
type fieldJSON struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// refusalJSON is the body of every failed response: one word for what went
// wrong, and for a body that was refused, what was wrong with it.
type refusalJSON struct {
	Error    string      `json:"error"`
	Problems []fieldJSON `json:"problems,omitempty"`
}

// MaxFieldBytes bounds a field and a message as a response writes them. A
// field may be a key the request chose, which may be as long as the body.
const MaxFieldBytes = 512

// Refuse answers with the one shape every failure has: a word for what went
// wrong, and for a body that was refused, what was wrong with it. The field
// and the message go through [invisible.Shown]: the field may be a key of
// the request, and the message may quote a value from it.
func Refuse(w http.ResponseWriter, status int, word string, problems []Field) {
	out := make([]fieldJSON, 0, len(problems))
	for _, p := range problems {
		out = append(out, fieldJSON{
			Field:   invisible.Shown(p.Field, MaxFieldBytes),
			Message: invisible.Shown(p.Message, MaxFieldBytes),
		})
	}
	JSON(w, status, refusalJSON{Error: word, Problems: out})
}

// JSON writes body as the response, under status.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// The status line is already written, so a failed encode cannot become an
	// error response.
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // nothing to report it to
}
