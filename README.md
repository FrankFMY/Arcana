# Arcana

[![Go Reference](https://pkg.go.dev/badge/github.com/FrankFMY/arcana.svg)](https://pkg.go.dev/github.com/FrankFMY/arcana)
[![Go Report Card](https://goreportcard.com/badge/github.com/FrankFMY/arcana)](https://goreportcard.com/report/github.com/FrankFMY/arcana)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Reactive Graph Sync Engine for Go** — real-time data synchronization between PostgreSQL and clients.

Inspired by ZeroCache, Replicache, and Electric SQL — built for Go backends with a TypeScript SDK for frontends.

## Features

- **Embeddable library** — not a standalone service, imports directly into your Go app
- **Graph-based subscriptions** — define named SQL queries with typed parameters and table dependencies
- **Normalized in-memory store** — shared data across subscriptions with RefCount GC
- **JSON Patch diffs** — RFC 6902 compliant, minimal data over the wire
- **Two update streams** — `table_diff` (row data, broadcast) + `view_diff` (structure, per-client)
- **Reconnect sync** — catch-up via version history or full snapshot fallback
- **Auto pg_notify triggers** — generates PostgreSQL trigger functions for change detection
- **Pluggable transport** — Centrifugo (built-in), WebSocket, SSE behind a clean interface
- **Pluggable change detection** — explicit notify, PostgreSQL LISTEN/NOTIFY, or custom
- **TypeScript SDK** — reactive client with Svelte 5 adapter and codegen
- **Batch transport** — single HTTP call per invalidation cycle to Centrifugo
- **Production-ready** — retry with backoff, reconnect, UUID validation, configurable limits

## Installation

```bash
go get github.com/FrankFMY/arcana
```

## Quick Start

```go
package main

import (
    "context"
    "net/http"

    "github.com/FrankFMY/arcana"
    "github.com/jackc/pgx/v5/pgxpool"
)

func main() {
    pool, _ := pgxpool.New(context.Background(), "postgres://...")

    engine := arcana.New(arcana.Config{
        Pool: arcana.PgxQuerier(pool),
        Transport: arcana.NewCentrifugoTransport(arcana.CentrifugoConfig{
            APIURL: "http://localhost:8000",
            APIKey: "your-api-key",
        }),
        AuthFunc: func(r *http.Request) (*arcana.Identity, error) {
            return extractIdentity(r)
        },
    })

    engine.Register(UserList)
    engine.Start(context.Background())
    defer engine.Stop()

    mux := http.NewServeMux()
    mux.Handle("/arcana/", http.StripPrefix("/arcana", engine.Handler()))
    http.ListenAndServe(":8080", mux)
}
```

### Define a Graph

```go
var UserList = arcana.GraphDef{
    Key: "user_list",
    Params: arcana.ParamSchema{
        "org_id": arcana.ParamUUID().Required(),
        "limit":  arcana.ParamInt().Default(50),
    },
    Deps: []arcana.TableDep{
        {Table: "users", Columns: []string{"id", "name", "email"}},
    },
    Factory: func(ctx context.Context, q arcana.Querier, p arcana.Params) (*arcana.Result, error) {
        wsID := arcana.WorkspaceID(ctx)

        rows, err := q.Query(ctx,
            "SELECT id, name, email FROM users WHERE workspace_id = $1 AND org_id = $2 ORDER BY name LIMIT $3",
            wsID, p.UUID("org_id"), p.Int("limit"),
        )
        if err != nil {
            return nil, err
        }
        defer rows.Close()

        result := arcana.NewResult()
        for rows.Next() {
            var id, name, email string
            rows.Scan(&id, &name, &email)
            result.AddRow("users", id, map[string]any{"id": id, "name": name, "email": email})
            result.AddRef(arcana.Ref{Table: "users", ID: id, Fields: []string{"id", "name", "email"}})
        }
        return result, nil
    },
}
```

### Notify on Changes

```go
func (r *UserRepo) Update(ctx context.Context, id, name string) error {
    _, err := r.pool.Exec(ctx, "UPDATE users SET name = $1 WHERE id = $2", name, id)
    if err == nil {
        engine.NotifyTable(ctx, "users", id, []string{"name"})
    }
    return err
}
```

### Automatic Change Detection with pg_notify

```go
// Auto-create PostgreSQL triggers for all registered tables
stmts := arcana.GenerateTriggerSQL("arcana_changes", engine.Registry().RepTable())
for _, sql := range stmts {
    pool.Exec(ctx, sql)
}

// Use PGNotifyListener for automatic change detection
engine := arcana.New(arcana.Config{
    ChangeDetector: arcana.NewPGNotifyListener(arcana.PGNotifyConfig{
        Conn:    pgConn,
        Channel: "arcana_changes",
    }),
    // ...
})
```

### TypeScript Client

```typescript
import { ArcanaClient } from "@arcana/client";

const arcana = new ArcanaClient({
    apiUrl: "/arcana",
    transport: centrifugoTransport,
});

const handle = await arcana.subscribe("user_list", {
    org_id: "550e8400-e29b-41d4-a716-446655440000",
    limit: 20,
});

// handle.data — reactive refs (auto-updated via table_diff + view_diff)
// arcana.getRow("users", "u1") — normalized row from shared store
```

## Architecture

```
Browser (TS SDK) ◄──── Centrifugo ◄──── Arcana Engine ◄──── PostgreSQL
                  table_diff              Registry
                  view_diff               DataStore
                                          Invalidator
                                          DiffEngine
```

**Two update streams:**
- `table_diff` — normalized row data changes, broadcast to workspace channel
- `view_diff` — view structure (refs) changes, sent to specific seance channel

**How it works:**
1. Client subscribes to a graph with parameters
2. Engine runs the Factory (SQL query), returns normalized refs + tables
3. When data changes (notify or pg_notify), Engine re-runs affected Factories
4. DiffEngine computes JSON Patch diffs (RFC 6902)
5. `table_diff` sent to workspace channel, `view_diff` sent to seance channel
6. Client SDK applies patches to local normalized store

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/subscribe` | Subscribe to a graph view |
| `POST` | `/unsubscribe` | Unsubscribe from a view |
| `POST` | `/sync` | Reconnect sync (catch-up or snapshot) |
| `GET` | `/active` | List active subscriptions for seance |
| `GET` | `/schema` | Representation table (for codegen) |
| `GET` | `/health` | Health check with stats |

## Configuration

```go
arcana.Config{
    Pool:                      querier,           // PostgreSQL pool (required)
    Transport:                 transport,          // Message delivery (required)
    AuthFunc:                  authFunc,           // Identity extraction from HTTP request
    ChangeDetector:            detector,           // nil = ExplicitNotifier
    InvalidationDebounce:      50 * time.Millisecond,  // Batch changes within window
    MaxSubscriptionsPerSeance: 100,                // Per-client subscription limit
    SnapshotThreshold:         50,                 // Version gap for full snapshot vs catch-up
    GCInterval:                time.Minute,        // Unreferenced row cleanup interval
}
```

## TypeScript Codegen

Generate type-safe TypeScript definitions from your Go graph registry:

```bash
go run ./cmd/arcana-gen -output ./sdk/generated/
```

Produces `tables.d.ts` and `views.d.ts` with full type information.

## Project Structure

```
arcana.go               Engine: New, Start, Stop, Notify, Handler
config.go               Configuration with sensible defaults
types.go                Core types: GraphDef, Ref, PatchOp, Params, ParamSchema
result.go               Factory result accumulator (AddRow, AddRef)
registry.go             Graph registry with inverted table→graph index
store.go                4-level normalized DataStore with RefCount GC
diff.go                 JSON Patch (RFC 6902) diff engine
invalidator.go          Change → Factory re-run → diff → transport pipeline
manager.go              Subscription lifecycle, sync (catch-up/snapshot)
handler.go              HTTP endpoints with auth middleware
transport.go            Transport interface
transport_centrifugo.go Centrifugo HTTP API (publish, batch, retry)
change.go               ChangeDetector interface
change_explicit.go      Explicit notify (default)
change_pgnotify.go      PostgreSQL LISTEN/NOTIFY with reconnect
pgnotify_triggers.go    Auto-generate PostgreSQL trigger functions
pgx_adapter.go          pgxpool.Pool → Querier adapter
codegen.go              TypeScript type generation
context.go              Identity context helpers
subscription.go         Subscription with version history
sdk/                    TypeScript client SDK with Svelte 5 adapter
cmd/arcana-gen/         Codegen CLI tool
examples/basic/         Minimal working example
```

## Testing

```bash
# Unit tests (fast, no Docker)
go test -short ./...

# Full suite including integration tests (requires Docker)
go test -race ./... -count=1

# Integration tests only
go test -run Integration -v -timeout 120s
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

[MIT](LICENSE)

## Author

Artem Pryanishnikov
- GitHub: [@FrankFMY](https://github.com/FrankFMY)
- Telegram: [@FrankFMY](https://t.me/FrankFMY)
