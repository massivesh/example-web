# example-web

A small Go web server (standard library only) for trying out minicoolify: zero-downtime deploys, scaling, request logs, app logs, env vars and build variables.

The home page shows which **replica** and **commit** answered, the app's **version** (a build variable), uptime, and the **request id** minicoolify's proxy added.

| endpoint | does |
|---|---|
| `GET /` | an HTML page with the above |
| `GET /api/info` | the same, as JSON |
| `GET /api/items?n=5` | a JSON list; `POST /api/items` with a JSON body returns 201 |
| `GET /slow?ms=1500` | answers after a delay (random 0.2–2s without `ms`) |
| `GET /status/{code}` | answers with any status; 5xx also logs an error to stderr |
| `GET /flaky` | fails (500) about one request in five |
| `GET /healthz` | 200 when ready, 503 while starting or shutting down |

It listens on `$PORT` (minicoolify sets it from the app's `port`; default 8000), logs JSON to stdout (errors to stderr) with each request's `X-Request-Id`, and on SIGTERM stops taking new requests and finishes the ones in flight.

## Add it to minicoolify

In the UI: **New app** → choose this repo from GitHub (or paste its URL) → type **Web**, port **8000**, health path **/healthz**, and either a domain (`web.example.com`) or a dedicated listen address (`:8080`).

Or with the API:

```bash
curl -X POST $API/apps -H "$H" -H 'Content-Type: application/json' -d '{
  "name": "example-web",
  "repo": "https://github.com/YOU/example-web.git",
  "port": 8000,
  "listen": ":8080",
  "health_path": "/healthz",
  "replicas": 2,
  "variables": {
    "GREETING": "Hello from minicoolify",
    "API_KEY": "sk_test_123",
    "APP_VERSION": {"value": "1.0.0", "build": true}
  }
}'
curl -X POST $API/apps/example-web/deploy -H "$H"
```

Then open `http://your-server:8080/` (or the domain).

### Variables

| variable | kind | effect |
|---|---|---|
| `GREETING` | runtime | the page's heading |
| `API_KEY` | runtime (secret) | never printed; the page shows only whether it arrived |
| `APP_VERSION` | **build** | baked into the binary (`version` on the page); needs a redeploy, not a restart |
| `STARTUP_DELAY` | runtime | e.g. `5s`: `/healthz` fails that long after start, like an app warming up |
| `FAIL_HEALTHCHECK` | runtime | `true`: never becomes healthy, so a deploy fails |

`PORT`, `SOURCE_COMMIT` and `MINICOOLIFY_APP` are set by minicoolify.

## Things to try

**Round robin.** Scale to 3 replicas, then reload the page: the replica changes.

**Zero-downtime deploy.** In one terminal, keep requesting:

```bash
while true; do curl -s -o /dev/null -w "%{http_code} " http://your-server:8080/api/info; sleep 0.1; done
```

Push a commit (or set `STARTUP_DELAY=5s` and redeploy). Every response stays 200 while the replicas are replaced; the page's commit changes when the new ones take over.

**A deploy that fails.** Set `FAIL_HEALTHCHECK=true` and redeploy. The new replicas never pass `/healthz`, the deploy is marked failed (see its log), and the old replicas keep serving. Remove the variable and redeploy to recover.

**Build variables.** Change `APP_VERSION` (a build variable): **Save & restart** keeps the old version, **Save & redeploy** shows the new one.

**Request logs.** Make some traffic:

```bash
for i in $(seq 50); do curl -s -o /dev/null http://your-server:8080/flaky; done
for ms in 100 800 2500; do curl -s -o /dev/null "http://your-server:8080/slow?ms=$ms"; done
curl -s -o /dev/null http://your-server:8080/status/404
curl -s -X POST -d '{"name": "x"}' http://your-server:8080/api/items
```

Then open the app's **Requests** tab: filter by **5xx**, or by path `/slow`; after a second deploy, compare the two commits' error rates and p95. The same over the API:

```bash
curl "$API/apps/example-web/requests?status=5xx&since=15m" -H "$H"
curl "$API/apps/example-web/requests?min_latency_ms=500" -H "$H"
curl "$API/apps/example-web/requests/summary?since=1h" -H "$H"
```

**Matching app logs to requests.** Every request log row has a `request_id`; the app logs the same id. Copy one from the Requests tab and search for it in **App logs** (grep box), or:

```bash
curl "$API/apps/example-web/logs?q=<request id>" -H "$H"
```

## Run it locally

```bash
go run .                                   # http://localhost:8000
docker build -t example-web --build-arg APP_VERSION=1.0.0 . && docker run -p 8000:8000 example-web
```
