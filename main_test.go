package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHealthzOK(t *testing.T) {
	prevHealthy, prevCommit := healthy.Load(), commit
	t.Cleanup(func() { healthy.Store(prevHealthy); commit = prevCommit })
	healthy.Store(true)
	commit = "abc123"

	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	want := map[string]string{"status": "ok", "commit": "abc123"}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v, want %v", body, want)
	}
}

func TestHealthzNotReady(t *testing.T) {
	prevHealthy := healthy.Load()
	t.Cleanup(func() { healthy.Store(prevHealthy) })
	healthy.Store(false)

	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
