package api

import (
	"net/http"

	"github.com/sucopay/sucopay/internal/payment"
)

// Webhooks serves the webhook endpoints of one account at a time, as
// [Payments] serves its payments. [webhook.HTTP] is one, and nil is the
// same nil as there: an instance configured without a database.
type Webhooks interface {
	Create(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	List(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	Read(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	Update(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	Rotate(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	Delete(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	Test(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
	Deliveries(w http.ResponseWriter, r *http.Request, account payment.AccountID) error
}

// Delivering is what the deployment's webhook deliveries have come to, in
// the one word a probe answers with. As with [Settling], no word takes the
// instance out of service: a deployment that delivers nothing still takes
// payments, and what a merchant is not told they can still read.
//
// Nil in an instance with no database, which delivers nothing.
type Delivering interface {
	Word() string
}

// delivering is the word, and nothing where nothing is delivered.
func delivering(d Delivering) string {
	if d == nil {
		return ""
	}
	return d.Word()
}
