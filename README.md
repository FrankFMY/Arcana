# Arcana

[![Go Reference](https://pkg.go.dev/badge/github.com/FrankFMY/arcana.svg)](https://pkg.go.dev/github.com/FrankFMY/arcana)
[![Go Report Card](https://goreportcard.com/badge/github.com/FrankFMY/arcana)](https://goreportcard.com/report/github.com/FrankFMY/arcana)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Reactive Graph Sync Engine for Go** — real-time data synchronization between PostgreSQL and any frontend via normalized diffs.

**[Website](https://frankfmy.github.io/Arcana/)** | **[Quick Start](#quick-start)** | **[API Reference](#api-reference)** | **[TypeScript SDK](sdk/)**

Inspired by ZeroCache, Replicache, and Electric SQL — built for Go backends with a TypeScript SDK for frontends.

## Features

- **Embeddable library** — not a standalone service, imports directly into your Go app
- **Graph-based subscriptions** — define named SQL queries with typed parameters and table dependencies
- **Normalized in-memory store** — shared data across subscriptions with RefCount GC
- **JSON Patch diffs** — RFC 6902 compliant, minimal data over the wire
- **Two update streams** — `table_diff` (row data, broadcast) + `view_diff` (structure, per-client)
- **Reconnect sync** — catch-up via version history or full snapshot fallback
- **Three change detection modes** — explicit notify, PostgreSQL LISTEN/NOTIFY with auto-triggers, WAL logical replication
- **Pluggable transport** — Centrifugo (built-in), or any custom WebSocket/SSE transport
- **TypeScript SDK** — reactive client with Svelte 5 adapter, offline persistence, and codegen
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
            return &arcana.Identity{
                SeanceID:    r.Header.Get("X-Seance-ID"),
                UserID:      "user-1",
                WorkspaceID: "workspace-1",
            }, nil
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

## Core Concepts

### Graphs

A **graph** is a named, parameterized SQL query that produces normalized data. When the underlying data changes, Arcana re-runs the query and pushes only the diff to subscribed clients.

```go
var UserList = arcana.GraphDef{
    Key: "user_list",

    // Typed parameters with validation
    Params: arcana.ParamSchema{
        "org_id": arcana.ParamUUID().Required(),
        "limit":  arcana.ParamInt().Default(50),
        "role":   arcana.ParamString().OneOf("admin", "member", "viewer").Build(),
    },

    // Declares which tables/columns this graph depends on.
    // Changes to these columns trigger re-evaluation.
    Deps: []arcana.TableDep{
        {Table: "users", Columns: []string{"id", "name", "email", "role"}},
    },

    // Factory runs the SQL query and returns normalized refs + rows.
    Factory: func(ctx context.Context, q arcana.Querier, p arcana.Params) (*arcana.Result, error) {
        wsID := arcana.WorkspaceID(ctx)

        rows, err := q.Query(ctx,
            `SELECT id, name, email, role FROM users
             WHERE workspace_id = $1 AND org_id = $2 ORDER BY name LIMIT $3`,
            wsID, p.UUID("org_id"), p.Int("limit"),
        )
        if err != nil {
            return nil, err
        }
        defer rows.Close()

        result := arcana.NewResult()
        for rows.Next() {
            var id, name, email, role string
            rows.Scan(&id, &name, &email, &role)

            // AddRow stores the normalized row data
            result.AddRow("users", id, map[string]any{
                "id": id, "name": name, "email": email, "role": role,
            })

            // AddRef defines the view structure (which rows belong to this view)
            result.AddRef(arcana.Ref{
                Table: "users", ID: id,
                Fields: []string{"id", "name", "email", "role"},
            })
        }
        return result, nil
    },
}
```

### Normalized Store

Arcana maintains a 4-level normalized in-memory store: `workspace -> table -> row_id -> fields`. Multiple subscriptions can reference the same row — a **RefCount GC** automatically cleans up rows no longer referenced by any subscription.

### Two Update Streams

When data changes, Arcana sends two types of messages:

| Stream | Channel | Content | Scope |
|--------|---------|---------|-------|
| `table_diff` | `workspace:{id}` | Row field changes (JSON Patch) | Broadcast to all clients in workspace |
| `view_diff` | `views:{seanceID}` | Refs structure changes + new row data | Per-client (seance) |

This separation means row data is sent once to the workspace (shared), while each client only receives structural changes relevant to their subscriptions.

### Identity & Context

Every subscription runs within an authenticated context:

```go
type Identity struct {
    SeanceID    string   // Unique client session ID
    UserID      string   // Authenticated user
    WorkspaceID string   // Tenant/organization scope
    Role        string   // User role
    Permissions []string // Fine-grained permissions
}
```

Inside a factory, use context helpers:

```go
Factory: func(ctx context.Context, q arcana.Querier, p arcana.Params) (*arcana.Result, error) {
    wsID := arcana.WorkspaceID(ctx)      // Workspace isolation
    user := arcana.User(ctx)             // Full identity
    if !user.HasPermission("read:users") {
        return nil, arcana.ErrForbidden
    }
    // ...
}
```

## Change Detection

Arcana supports three modes for detecting data changes:

### 1. Explicit Notify (Default)

Call `engine.Notify()` after database mutations. Simple, requires app instrumentation.

```go
func (r *UserRepo) Update(ctx context.Context, id, name string) error {
    _, err := r.pool.Exec(ctx, "UPDATE users SET name = $1 WHERE id = $2", name, id)
    if err == nil {
        engine.NotifyTable(ctx, "users", id, []string{"name"})
    }
    return err
}
```

### 2. PostgreSQL LISTEN/NOTIFY

Auto-generated triggers send change notifications via PostgreSQL's `NOTIFY` mechanism. Zero app instrumentation needed after setup.

```go
// Generate and install triggers for all registered tables
stmts := arcana.GenerateTriggerSQL("arcana_changes", engine.Registry().RepTable())
for _, sql := range stmts {
    pool.Exec(ctx, sql)
}
// Or use the convenience function:
arcana.EnsureTriggers(ctx, pool, "arcana_changes", engine.Registry().RepTable())

// Configure the engine to listen
engine := arcana.New(arcana.Config{
    ChangeDetector: arcana.NewPGNotifyListener(arcana.PGNotifyConfig{
        Conn:    pgConn,
        Channel: "arcana_changes",
    }),
    // ...
})
```

**PGNotifyListener features:**
- Automatic reconnect with exponential backoff
- Configurable max retries
- JSON payload parsing with table/row/column extraction

### 3. WAL Logical Replication

Listens to PostgreSQL's logical replication stream (pgoutput plugin). Most reliable — captures all DML operations with zero app changes. Requires `wal_level=logical`.

```go
engine := arcana.New(arcana.Config{
    ChangeDetector: arcana.NewWALListener(arcana.WALConfig{
        ConnString:     "postgres://user:pass@localhost:5432/mydb",
        SlotName:       "arcana_slot",     // default
        Publication:    "arcana_pub",      // default
        Tables:         []string{"users", "orders"}, // or empty for all
        StandbyTimeout: 10 * time.Second,  // default
    }),
    // ...
})
```

**WALListener features:**
- Automatic publication and replication slot creation
- Relation cache for column name resolution
- Changed column detection (only reports actually modified columns)
- Standby status updates for PostgreSQL keepalive

**Note:** You can combine modes. When using `PGNotifyListener` or `WALListener` as the `ChangeDetector`, explicit `engine.Notify()` calls still work — both paths feed into the same invalidation pipeline.

## Transport

The `Transport` interface abstracts message delivery to clients:

```go
type Transport interface {
    SendToSeance(ctx context.Context, seanceID string, msg Message) error
    SendToWorkspace(ctx context.Context, workspaceID string, msg Message) error
    DisconnectSeance(ctx context.Context, seanceID string) error
}
```

### Centrifugo Transport (Built-in)

```go
transport := arcana.NewCentrifugoTransport(arcana.CentrifugoConfig{
    APIURL:     "http://localhost:8000", // Base URL (without /api — appended automatically)
    APIKey:     "your-centrifugo-api-key",
    HTTPClient: &http.Client{Timeout: 5 * time.Second}, // optional
    Retries:    3, // optional, default: 0
})
```

**Important:** The Centrifugo transport appends `/api` to the `APIURL` internally. Pass the base URL (e.g., `http://localhost:8000`), not `http://localhost:8000/api`.

Features:
- **Batch publishing** — sends multiple messages in a single HTTP call via `/api/batch`
- **Retry with exponential backoff** — configurable retry count (100ms -> 500ms -> 2.5s)
- **Channel naming** — `workspace:{id}` for table diffs, `views:{seanceID}` for view diffs

### Custom Transport

Implement the `Transport` interface for any delivery mechanism (raw WebSocket, SSE, gRPC streams):

```go
type MyTransport struct{}

func (t *MyTransport) SendToSeance(ctx context.Context, seanceID string, msg arcana.Message) error {
    // Deliver msg to the specific client session
    return nil
}

func (t *MyTransport) SendToWorkspace(ctx context.Context, wsID string, msg arcana.Message) error {
    // Broadcast msg to all clients in the workspace
    return nil
}

func (t *MyTransport) DisconnectSeance(ctx context.Context, seanceID string) error {
    // Force-disconnect a client session
    return nil
}
```

## HTTP API

Mount the handler on your router:

```go
mux.Handle("/arcana/", http.StripPrefix("/arcana", engine.Handler()))
```

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/subscribe` | Subscribe to a graph view |
| `POST` | `/unsubscribe` | Unsubscribe from a view |
| `POST` | `/sync` | Reconnect sync (catch-up or snapshot) |
| `GET` | `/active` | List active subscriptions for the current seance |
| `GET` | `/schema` | Representation table (all tables/columns across graphs) |
| `GET` | `/health` | Health check with engine stats |

All endpoints require authentication via `AuthFunc`. Responses use the envelope format: `{"ok": true, "data": {...}}` or `{"ok": false, "error": "..."}`.

### Subscribe

```
POST /subscribe
{
    "view": "user_list",
    "params": {"org_id": "550e8400-...", "limit": 20}
}

Response:
{
    "ok": true,
    "data": {
        "params_hash": "a1b2c3d4",
        "version": 1,
        "refs": [
            {"table": "users", "id": "u1", "fields": ["id", "name", "email"]}
        ],
        "tables": {
            "users": {
                "u1": {"id": "u1", "name": "Alice", "email": "alice@example.com"}
            }
        },
        "total": 150
    }
}

// "total" is included when the factory calls result.SetTotal(n).
// Useful for paginated views where clients need the full count.
```

### Sync (Reconnect)

When a client reconnects, it sends its last known version for each view. The engine responds with either catch-up patches or a full snapshot:

```
POST /sync
{
    "views": [
        {"view": "user_list", "params_hash": "a1b2c3d4", "version": 3}
    ]
}

Response (catch-up mode):
{
    "ok": true,
    "data": {
        "views": [{
            "view": "user_list",
            "params_hash": "a1b2c3d4",
            "mode": "catch_up",
            "patches": [
                {"version": 4, "refs_patch": [...], "tables": {...}},
                {"version": 5, "refs_patch": [...], "tables": {...}}
            ]
        }]
    }
}

Response (snapshot mode — version gap too large):
{
    "ok": true,
    "data": {
        "views": [{
            "view": "user_list",
            "params_hash": "a1b2c3d4",
            "mode": "snapshot",
            "version": 42,
            "refs": [...],
            "tables": {...}
        }]
    }
}
```

The `SnapshotThreshold` config controls when sync falls back to a full snapshot (default: 50 versions).

## Configuration

```go
arcana.Config{
    // Required
    Pool:      arcana.PgxQuerier(pool),              // PostgreSQL connection pool
    Transport: transport,                             // Message delivery

    // Authentication
    AuthFunc:  func(r *http.Request) (*arcana.Identity, error) { ... },

    // Change detection (nil = ExplicitNotifier)
    ChangeDetector: nil,

    // Tuning
    InvalidationDebounce:      50 * time.Millisecond, // Batch changes within window
    MaxSubscriptionsPerSeance: 100,                    // Per-client subscription limit
    SnapshotThreshold:         50,                     // Version gap for full snapshot vs catch-up
    GCInterval:                time.Minute,            // Unreferenced row cleanup interval
}
```

## Params API

### Defining Parameters

```go
arcana.ParamSchema{
    "org_id":   arcana.ParamUUID().Required(),            // Required UUID
    "limit":    arcana.ParamInt().Default(50),             // Optional int with default
    "status":   arcana.ParamString().OneOf("active", "archived").Build(), // Enum
    "verbose":  arcana.ParamBool().Default(false),         // Optional boolean
    "min_price": arcana.ParamFloat().Build(),              // Optional float
}
```

### Using Parameters in Factories

```go
Factory: func(ctx context.Context, q arcana.Querier, p arcana.Params) (*arcana.Result, error) {
    orgID := p.UUID("org_id")       // string
    limit := p.Int("limit")         // int (50 if not provided)
    status := p.String("status")    // string
    verbose := p.Bool("verbose")    // bool
    minPrice := p.Float("min_price") // float64
    raw := p.Raw()                  // map[string]any
    // ...
}
```

### Validating Parameters

```go
resolved, err := arcana.ValidateParams(schema, rawInput)
// strict mode rejects unknown parameters:
resolved, err := arcana.ValidateParams(schema, rawInput, true)
```

## Result API

```go
result := arcana.NewResult()

// Add a normalized row (table, row_id, fields)
result.AddRow("users", "u1", map[string]any{
    "id": "u1", "name": "Alice", "email": "alice@example.com",
})

// Add a ref (defines view structure — which rows belong to this view)
result.AddRef(arcana.Ref{
    Table: "users", ID: "u1",
    Fields: []string{"id", "name", "email"},
})

// Nested refs for hierarchical data
result.AddRef(arcana.Ref{
    Table: "orders", ID: "o1",
    Fields: []string{"id", "total"},
    Nested: map[string]arcana.Ref{
        "customer": {Table: "users", ID: "u1", Fields: []string{"id", "name"}},
    },
})

// Pagination: set total count for paginated results
result.SetTotal(150) // total matching rows (before LIMIT/OFFSET)

// Inspect
result.RowCount()  // number of rows added
result.Refs()      // all refs
result.Tables()    // map[table]map[rowID]map[field]any
result.Total()     // total count set via SetTotal (0 if not set)
```

## Errors

```go
arcana.ErrForbidden            // 403 — permission denied
arcana.ErrNotFound             // 404 — graph or subscription not found
arcana.ErrInvalidParams        // 400 — parameter validation failed
arcana.ErrTooManySubscriptions // 429 — exceeded MaxSubscriptionsPerSeance
arcana.ErrAlreadyStarted       // engine.Start() called twice
arcana.ErrNotStarted           // engine.Stop() called before Start()
```

## Engine Stats

```go
stats := engine.Stats()
// EngineStats{
//     Running:             true,
//     RegisteredGraphs:    5,
//     ActiveSubscriptions: 42,
//     SeancesWithSubs:     12,
//     DataStoreRows:       350,
// }
```

## TypeScript Codegen

Generate type-safe TypeScript definitions from your Go graph registry:

```bash
go run ./cmd/arcana-gen -output ./sdk/generated/
```

Produces `tables.d.ts` (all table schemas) and `views.d.ts` (graph parameters and dependencies).

## Database Adapter

Arcana uses a `Querier` interface compatible with any SQL driver:

```go
type Querier interface {
    Query(ctx context.Context, sql string, args ...any) (Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) Row
}
```

For pgx v5:

```go
pool, _ := pgxpool.New(ctx, connStr)
querier := arcana.PgxQuerier(pool)
```

## Project Structure

```
arcana.go                  Engine: New, Start, Stop, Notify, Handler, Stats
config.go                  Configuration with sensible defaults
types.go                   Core types: GraphDef, Ref, PatchOp, Params, ParamSchema, Identity
result.go                  Factory result accumulator (AddRow, AddRef, SetTotal)
errors.go                  Exported error values
context.go                 Identity context helpers (WithIdentity, WorkspaceID, User)
registry.go                Graph registry with inverted table->graph index
store.go                   4-level normalized DataStore with RefCount GC
diff.go                    JSON Patch (RFC 6902) diff engine
invalidator.go             Change -> Factory re-run -> diff -> transport pipeline
manager.go                 Subscription lifecycle, sync (catch-up/snapshot)
subscription.go            Subscription with version history ring buffer
handler.go                 HTTP endpoints (/subscribe, /unsubscribe, /sync, /active, /schema, /health)
middleware.go              Auth middleware for HTTP handler
transport.go               Transport interface
transport_centrifugo.go    Centrifugo HTTP API (publish, batch, retry with backoff)
change.go                  ChangeDetector interface
change_explicit.go         Explicit notify (default, channel-based)
change_pgnotify.go         PostgreSQL LISTEN/NOTIFY with auto-reconnect
change_wal.go              PostgreSQL WAL logical replication (pgoutput)
pgnotify_triggers.go       Auto-generate PostgreSQL trigger functions
pgx_adapter.go             pgxpool.Pool -> Querier adapter
codegen.go                 TypeScript type generation
sdk/                       TypeScript client SDK with Svelte 5 adapter
cmd/arcana-gen/            Codegen CLI tool
examples/basic/            Minimal working example
```

## Testing

```bash
# Unit tests (fast, no Docker)
go test -short ./...

# Full suite including integration tests (requires Docker for testcontainers)
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
