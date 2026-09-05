package api_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/api"
)

// quiet is a logger for a test that is not about the log.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// recorder is a logger and the lines it wrote. The handler runs on the
// serving goroutine, so the buffer is guarded.
type recorder struct {
	mu    sync.Mutex
	lines bytes.Buffer
}

func (r *recorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lines.Write(b)
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lines.String()
}

func (r *recorder) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(r, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// jsonLogger writes the format a deployment writes. It matters which: the text
// handler would escape a quoting bug out of sight, passing the check whether
// or not the value was quoted first.
func (r *recorder) jsonLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(r, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestHandler_WritesOneLineSayingWhatHappened(t *testing.T) {
	t.Parallel()
	log := &recorder{}
	rec := httptest.NewRecorder()

	api.Handler(log.logger(), api.Dependencies{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	line := log.String()
	for _, want := range []string{
		"msg=served", "method=GET", "path=/healthz", "status=200", "request_id=",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the line does not carry %s:\n%s", want, line)
		}
	}
}

func TestHandler_KeepsTheQueryOutOfTheLine(t *testing.T) {
	t.Parallel()
	// Whoever is asking writes the query, and it is where a token or an
	// identifier ends up. The path is enough to say which route was reached.
	log := &recorder{}
	rec := httptest.NewRecorder()

	api.Handler(log.logger(), api.Dependencies{}).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/healthz?token=hunter2", nil))

	if strings.Contains(log.String(), "hunter2") {
		t.Errorf("the line carries the query:\n%s", log.String())
	}
}

func TestHandler_QuotesAPathAWholeRequestChose(t *testing.T) {
	t.Parallel()
	// A path is written by whoever is asking, and a line laid out in fields
	// is one a newline would forge a field in. Short on purpose: a payload
	// past the length limit would be cut away before quoting could matter,
	// and the test would pass without the quoting.
	//
	// Percent-encoded, which is how these reach URL.Path: net/http refuses
	// them written into the request line itself.
	for _, c := range []struct{ name, target string }{
		{"a newline", "/a%0a%20%20msg=forged"},
		{"a right-to-left override", "/a%e2%80%aeb"},
		{"an escape sequence", "/a%1b%5b31mb"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			log := &recorder{}

			api.Handler(log.jsonLogger(), api.Dependencies{}).ServeHTTP(httptest.NewRecorder(),
				httptest.NewRequest(http.MethodGet, c.target, nil))

			line := log.String()
			if strings.Count(line, "\n") != 1 {
				t.Errorf("one request wrote %d lines:\n%q", strings.Count(line, "\n"), line)
			}
			for _, r := range line {
				if r != '\n' && (r < 0x20 || r == 0x202e) {
					t.Errorf("the line carries %U:\n%q", r, line)
				}
			}
		})
	}
}

func TestHandler_CutsAPathLongerThanALineShouldBe(t *testing.T) {
	t.Parallel()
	log := &recorder{}

	api.Handler(log.logger(), api.Dependencies{}).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/"+strings.Repeat("a", 4000), nil))

	if line := log.String(); len(line) > 1000 {
		t.Errorf("one request wrote %d bytes", len(line))
	}
}

func TestHandler_NamesEachRequestSeparately(t *testing.T) {
	t.Parallel()
	log := &recorder{}
	h := api.Handler(log.logger(), api.Dependencies{})

	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}

	ids := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		for _, field := range strings.Fields(line) {
			if id, found := strings.CutPrefix(field, "request_id="); found {
				ids[id] = true
			}
		}
	}
	if len(ids) != 2 {
		t.Errorf("two requests were named %d ways: %v", len(ids), ids)
	}
}

func TestReadyz_ReportsTheDatabaseAndHealthzDoesNot(t *testing.T) {
	t.Parallel()
	// A liveness probe that failed because a database was unreachable would
	// have an orchestrator restart an instance that is working.
	log := &recorder{}
	down := func(context.Context) error { return errors.New("no route to host") }
	h := api.Handler(log.logger(), api.Dependencies{Database: down})

	alive := httptest.NewRecorder()
	h.ServeHTTP(alive, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	ready := httptest.NewRecorder()
	h.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if alive.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want %d while the process is running", alive.Code, http.StatusOK)
	}
	if ready.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d, want %d", ready.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(ready.Body.String(), "no route to host") {
		t.Errorf("the reason was answered over HTTP: %s", ready.Body)
	}
	if !strings.Contains(log.String(), "no route to host") {
		t.Errorf("the reason reached nobody:\n%s", log.String())
	}
}

func TestReadyz_SaysWhenNoDatabaseIsConfigured(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	api.Handler(quiet(), api.Dependencies{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("/readyz = %d, want %d: no database is a state, not a failure", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "none configured") {
		t.Errorf("body = %s, want it to say there is none", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "credentials") {
		t.Errorf("body = %s, want nothing said of credentials it has nowhere to look for", rec.Body)
	}
}

func TestHandler_MeasuresHowLongTheRequestTook(t *testing.T) {
	t.Parallel()
	// The field being present says nothing: a constant would satisfy that.
	log := &recorder{}
	slow := func(context.Context) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}

	api.Handler(log.logger(), api.Dependencies{Database: slow}).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/readyz", nil))

	got := field(t, log.String(), "duration_ms")
	ms, err := strconv.Atoi(got)
	if err != nil {
		t.Fatalf("duration_ms = %q: %v", got, err)
	}
	if ms < 10 {
		t.Errorf("duration_ms = %d for a request that took at least 20ms", ms)
	}
}

// field returns the value of one key in a text-handler line.
func field(t *testing.T, line, key string) string {
	t.Helper()
	for _, f := range strings.Fields(line) {
		if v, found := strings.CutPrefix(f, key+"="); found {
			return v
		}
	}
	t.Fatalf("no %s in:\n%s", key, line)
	return ""
}

func TestHandler_CutsAMethodLongerThanAnyMethodIs(t *testing.T) {
	t.Parallel()
	// net/http answers a request whose method is thousands of bytes, and the
	// method is written by whoever is asking, the same as the path.
	log := &recorder{}
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Method = strings.Repeat("A", 8000)

	api.Handler(log.logger(), api.Dependencies{}).ServeHTTP(httptest.NewRecorder(), r)

	if line := log.String(); len(line) > 1000 {
		t.Errorf("one request wrote %d bytes", len(line))
	}
}
