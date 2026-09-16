package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// The headers a delivery carries, as Standard Webhooks names them. The id
// and the time sent are signed with the body, so that neither can be
// replaced without the signature failing.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// Timestamp is the time sent as the header carries it: seconds since the
// epoch, in decimal.
func Timestamp(sent time.Time) string { return strconv.FormatInt(sent.Unix(), 10) }

// Sign is the signature header for body sent as delivery id at the time the
// timestamp header carries, under each of secrets: a version and a base64
// HMAC-SHA256 per secret, separated by a space. Two secrets are the one in
// force and the one it replaced within its grace, and a receiver that has
// moved to either verifies.
//
// What is signed is the id, the timestamp and the body joined with dots,
// the body as the bytes on the wire, which is what the specification's
// verification recomputes.
func Sign(secrets []Secret, id ID, timestamp string, body []byte) (string, error) {
	signed := make([]string, 0, len(secrets))
	for _, s := range secrets {
		key, err := s.Key()
		if err != nil {
			return "", err
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(string(id) + "." + timestamp + "."))
		mac.Write(body)
		signed = append(signed, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}
	return strings.Join(signed, " "), nil
}
