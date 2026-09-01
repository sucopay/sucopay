package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
)

// What a request can put in a log line. Both are written by whoever is asking
// and neither is otherwise bounded: a method of eight thousand bytes is a
// request net/http will answer.
const (
	maxMethodBytes = 32
	maxPathBytes   = 256
)

type requestIDKey struct{}

// logger returns the logger for one request, which carries that request's
// identifier. Every line about one request can then be found from any other.
func logger(ctx context.Context, log *slog.Logger) *slog.Logger {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return log.With(slog.String("request_id", id))
	}
	return log
}

// record answers every request and writes one line saying what happened.
//
// The line names the request rather than repeating it: the method, the route
// as this process wrote it, and the path cut short and quoted. Whoever sent it
// chose the path, and a report is a line a terminal acts on.
func record(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if id, err := requestID(); err == nil {
			ctx = context.WithValue(ctx, requestIDKey{}, id)
			r = r.WithContext(ctx)
		} else {
			// Nothing about the request needs an identifier to be answered.
			// An empty one would be worse than none: every request without an
			// identifier would share it.
			log.WarnContext(ctx, "could not name this request", slog.Any("error", err))
		}

		began := time.Now()
		counted := &counter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(counted, r)

		logger(ctx, log).InfoContext(ctx, "served",
			slog.String("method", invisible.Shown(r.Method, maxMethodBytes)),
			slog.String("path", invisible.Shown(r.URL.Path, maxPathBytes)),
			slog.Int("status", counted.status),
			slog.Int64("duration_ms", time.Since(began).Milliseconds()),
		)
	})
}

func requestID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// counter remembers the status the handler wrote, which is otherwise gone by
// the time the request is over.
type counter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (c *counter) WriteHeader(status int) {
	if !c.written {
		c.status = status
		c.written = true
	}
	c.ResponseWriter.WriteHeader(status)
}

// Unwrap hands back what this wraps. Embedding the interface promotes only
// the three methods it declares, so a handler that needed to flush or hijack
// would find a writer that could do neither, and http.NewResponseController
// looks for this method to reach past a wrapper like this one.
func (c *counter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *counter) Write(b []byte) (int, error) {
	c.written = true
	return c.ResponseWriter.Write(b)
}
