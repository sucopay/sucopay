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
}
