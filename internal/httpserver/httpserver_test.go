package httpserver

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaMoja/xibalba/internal/health"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func hello() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") })
}

func TestServerLifecycle(t *testing.T) {
	s := New(Options{Name: "test", Addr: "127.0.0.1:0", Handler: hello(), Log: quiet()})

	if got := s.Health().State; got != health.Down {
		t.Errorf("health before start = %q, want down", got)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := s.Health().State; got != health.OK {
		t.Errorf("health after start = %q, want ok", got)
	}

	resp, err := http.Get("http://" + s.Addr() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if s.Addr() != "" || s.Health().State != health.Down {
		t.Errorf("after stop: addr = %q, health = %+v", s.Addr(), s.Health())
	}
}

func TestStartFailsWhenAddressIsTaken(t *testing.T) {
	first := New(Options{Name: "first", Addr: "127.0.0.1:0", Handler: hello(), Log: quiet()})
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Stop(context.Background()) }()

	second := New(Options{Name: "second", Addr: first.Addr(), Handler: hello(), Log: quiet()})
	err := second.Start(context.Background())
	if err == nil {
		_ = second.Stop(context.Background())
		t.Fatal("second server started on a taken address")
	}
	if !strings.Contains(err.Error(), first.Addr()) {
		t.Errorf("error should name the address: %v", err)
	}
}

func TestRecoverContainsPanicToOneRequest(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(http.ResponseWriter, *http.Request) { panic("secret internal detail") })
	mux.Handle("/ok", hello())
	h := Recover(log, mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("panic detail leaked to the client: %q", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "secret internal detail") || !strings.Contains(logs.String(), "/boom") {
		t.Errorf("panic was not logged with its cause and path: %s", logs.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("request after a panic got %d, want 200", rec.Code)
	}
}
