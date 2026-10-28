// example-web: a small HTTP server for trying out minicoolify.
//
// It shows which replica and commit answered, echoes the X-Request-Id the
// proxy adds (so its own logs can be matched to minicoolify's request logs),
// and has endpoints that are slow, fail or log to stderr on demand, to see
// them in the Requests and App logs tabs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// version is baked in at build time from the APP_VERSION build variable
// (see the Dockerfile): set it as a build var in minicoolify and redeploy.
var version = "dev"

var (
	started  = time.Now()
	hostname = must(os.Hostname()) // the container id: which replica answered
	commit   = os.Getenv("SOURCE_COMMIT")
	served   atomic.Int64
	healthy  atomic.Bool
)

func main() {
	port := envOr("PORT", "8000")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	errLog := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", home)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "starting or shutting down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, info(r))
	})
	mux.HandleFunc("GET /api/items", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		n = min(max(n, 1), 100)
		items := make([]map[string]any, n)
		for i := range items {
			items[i] = map[string]any{"id": i + 1, "name": fmt.Sprintf("item %d", i+1)}
		}
		writeJSON(w, 200, map[string]any{"items": items, "replica": hostname})
	})
	mux.HandleFunc("POST /api/items", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "body must be JSON: " + err.Error()})
			return
		}
		writeJSON(w, 201, map[string]any{"created": body, "replica": hostname})
	})
	// /slow?ms=1500: answers after a delay (try min_latency_ms in the request log filters)
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		if ms <= 0 {
			ms = 200 + rand.IntN(1800)
		}
		select {
		case <-time.After(time.Duration(min(ms, 30_000)) * time.Millisecond):
			writeJSON(w, 200, map[string]any{"slept_ms": ms})
		case <-r.Context().Done():
		}
	})
	// /status/503: answers with any status (try the 4xx/5xx filters)
	mux.HandleFunc("GET /status/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.PathValue("code"))
		if err != nil || code < 200 || code > 599 {
			http.Error(w, "code must be 200-599", 400)
			return
		}
		if code >= 500 {
			errLog.Error("returning an error on purpose", "status", code, "request_id", r.Header.Get("X-Request-Id"))
		}
		writeJSON(w, code, map[string]any{"status": code})
	})
	// /flaky: fails about one request in five
	mux.HandleFunc("GET /flaky", func(w http.ResponseWriter, r *http.Request) {
		if rand.IntN(5) == 0 {
			errLog.Error("flaky endpoint failed", "request_id", r.Header.Get("X-Request-Id"))
			writeJSON(w, 500, map[string]string{"error": "unlucky"})
			return
		}
		writeJSON(w, 200, map[string]string{"result": "lucky"})
	})

	srv := &http.Server{Addr: ":" + port, Handler: logRequests(logger, mux), ReadHeaderTimeout: 10 * time.Second}

	// Simulated warm-up: /healthz fails until it's done, so minicoolify waits
	// before sending traffic to a new replica (STARTUP_DELAY=5s to see it).
	go func() {
		d, _ := time.ParseDuration(envOr("STARTUP_DELAY", "0s"))
		time.Sleep(d)
		if os.Getenv("FAIL_HEALTHCHECK") == "true" {
			logger.Warn("FAIL_HEALTHCHECK is set: staying unhealthy, so this deploy will fail and the old replicas keep running")
			return
		}
		healthy.Store(true)
		logger.Info("ready")
	}()

	go func() {
		logger.Info("listening", "port", port, "version", version, "commit", commit, "replica", hostname)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errLog.Error("server stopped", "err", err)
			os.Exit(1)
		}
	}()

	// minicoolify sends SIGTERM after it has taken this replica out of the
	// load balancer; finish in-flight requests within stop_timeout.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	healthy.Store(false)
	logger.Info("SIGTERM: draining in-flight requests", "served", served.Load())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		errLog.Error("shutdown", "err", err)
	}
	logger.Info("stopped")
}

// logRequests logs each request as the app sees it, with the request id
// minicoolify's proxy added, so the two logs can be joined.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		served.Add(1)
		if r.URL.Path != "/healthz" {
			logger.Info("handled", "method", r.Method, "path", r.URL.Path,
				"request_id", r.Header.Get("X-Request-Id"), "ms", time.Since(start).Milliseconds())
		}
	})
}

func info(r *http.Request) map[string]any {
	greeting := envOr("GREETING", "Hello from minicoolify")
	_, hasSecret := os.LookupEnv("API_KEY")
	return map[string]any{
		"greeting":        greeting,
		"app":             os.Getenv("MINICOOLIFY_APP"),
		"version":         version,
		"commit":          commit,
		"replica":         hostname,
		"uptime":          time.Since(started).Round(time.Second).String(),
		"requests_served": served.Load(),
		"request_id":      r.Header.Get("X-Request-Id"),
		"client":          r.Header.Get("X-Forwarded-For"),
		"proto":           r.Header.Get("X-Forwarded-Proto"),
		"api_key_is_set":  hasSecret, // never print secrets, just whether they arrived
		"host":            r.Host,
		"forwarded_host":  r.Header.Get("X-Forwarded-Host"),
	}
}

var page = template.Must(template.New("home").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.app}} · example-web</title>
<style>
  body { font: 15px/1.5 ui-sans-serif, system-ui, sans-serif; background: #0a0c0d; color: #e6e8e9; margin: 0; padding: 40px 20px; }
  main { max-width: 640px; margin: auto; }
  h1 { font-size: 22px; margin: 0 0 4px; }
  p { color: #9ba3a8; margin: 0 0 24px; }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 6px 20px; background: #111416; border: 1px solid #262b2f; border-radius: 4px; padding: 16px 20px; }
  dt { color: #9ba3a8; } dd { margin: 0; font-family: ui-monospace, monospace; word-break: break-all; }
  a { color: #f5a524; } ul { padding-left: 20px; color: #9ba3a8; } code { color: #e6e8e9; }
</style></head>
<body><main>
  <h1>{{.greeting}}</h1>
  <p>Reload a few times: with more than one replica, the replica changes.</p>
  <dl>
    <dt>replica</dt><dd>{{.replica}}</dd>
    <dt>commit</dt><dd>{{if .commit}}{{.commit}}{{else}}(not set){{end}}</dd>
    <dt>version</dt><dd>{{.version}}</dd>
    <dt>uptime</dt><dd>{{.uptime}}</dd>
    <dt>requests served</dt><dd>{{.requests_served}}</dd>
    <dt>request id</dt><dd>{{.request_id}}</dd>
    <dt>API_KEY set</dt><dd>{{.api_key_is_set}}</dd>
  </dl>
  <p></p>
  <ul>
    <li><a href="/api/info">/api/info</a>: the same, as JSON</li>
    <li><a href="/api/items?n=5">/api/items?n=5</a>: a JSON list (POST JSON to create)</li>
    <li><a href="/slow?ms=1500">/slow?ms=1500</a>: a slow response</li>
    <li><a href="/status/503">/status/503</a>: any status code, 5xx also logs to stderr</li>
    <li><a href="/flaky">/flaky</a>: fails one time in five</li>
  </ul>
</main></body></html>`))

func home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page.Execute(w, info(r))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
