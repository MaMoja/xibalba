package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

type fake struct {
	name     string
	startErr error
	stopErr  error
	panics   bool
	events   *[]string
}

func (f *fake) Name() string { return f.name }
func (f *fake) Start(context.Context) error {
	if f.panics {
		panic("start exploded")
	}
	if f.startErr == nil {
		*f.events = append(*f.events, "start "+f.name)
	}
	return f.startErr
}
func (f *fake) Stop(context.Context) error {
	*f.events = append(*f.events, "stop "+f.name)
	return f.stopErr
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestStartAndStopOrder(t *testing.T) {
	var events []string
	s := New(quiet())
	for _, n := range []string{"a", "b", "c"} {
		s.Add(&fake{name: n, events: &events})
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []string{"start a", "start b", "start c", "stop c", "stop b", "stop a"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}
}

func TestStartFailureRollsBackAndNamesComponent(t *testing.T) {
	var events []string
	s := New(quiet())
	s.Add(&fake{name: "a", events: &events})
	s.Add(&fake{name: "b", events: &events, startErr: errors.New("port in use")})
	s.Add(&fake{name: "c", events: &events})

	err := s.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded, want an error")
	}
	if !strings.Contains(err.Error(), `"b"`) || !strings.Contains(err.Error(), "port in use") {
		t.Errorf("error does not name the component and cause: %v", err)
	}
	want := []string{"start a", "stop a"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %v, want %v (c must never start)", events, want)
	}
}

func TestPanicInStartIsAnError(t *testing.T) {
	var events []string
	s := New(quiet())
	s.Add(&fake{name: "wild", events: &events, panics: true})

	err := s.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), `"wild"`) || !strings.Contains(err.Error(), "start exploded") {
		t.Errorf("got %v, want an error naming the component and the panic", err)
	}
}

func TestStopTriesEveryComponent(t *testing.T) {
	var events []string
	s := New(quiet())
	s.Add(&fake{name: "a", events: &events, stopErr: errors.New("a stuck")})
	s.Add(&fake{name: "b", events: &events, stopErr: errors.New("b stuck")})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := s.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "a stuck") || !strings.Contains(err.Error(), "b stuck") {
		t.Errorf("got %v, want both stop errors", err)
	}
}

func TestReporterDeliversFailureAndNeverBlocks(t *testing.T) {
	s := New(quiet())
	report := s.Reporter("listener")
	cause := errors.New("socket closed")

	for i := 0; i < 100; i++ { // far more than the buffer holds
		report(cause)
	}
	f := <-s.Failures()
	if f.Component != "listener" || !errors.Is(f, cause) {
		t.Errorf("failure = %+v", f)
	}
}
