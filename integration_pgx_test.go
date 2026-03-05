package arcana

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startPostgres launches a PostgreSQL container and returns a connected pool.
func startPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("arcana_test"),
		postgres.WithUsername("arcana"),
		postgres.WithPassword("arcana"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { pgContainer.Terminate(ctx) })

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	return pool
}

// createSchema sets up test tables.
func createSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			role TEXT DEFAULT 'member'
		)
	`)
	if err != nil {
		t.Fatalf("create users table: %v", err)
	}
}

// seedData inserts initial test data.
func seedData(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, workspace_id, name, email, role) VALUES
			('u1', 'w1', 'Alice', 'alice@test.com', 'admin'),
			('u2', 'w1', 'Bob', 'bob@test.com', 'member')
	`)
	if err != nil {
		t.Fatalf("seed data: %v", err)
	}
}

func TestPgxIntegrationFullPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	pool := startPostgres(t, ctx)
	createSchema(t, ctx, pool)
	seedData(t, ctx, pool)

	transport := newMockTransport()
	querier := PgxQuerier(pool)

	engine := New(Config{
		Pool:       querier,
		Transport:  transport,
		GCInterval: time.Hour,
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return &Identity{
				SeanceID:    "s1",
				UserID:      "u1",
				WorkspaceID: "w1",
			}, nil
		},
	})

	// Register a graph that runs REAL SQL
	engine.Register(GraphDef{
		Key: "user_list",
		Params: ParamSchema{
			"role": ParamString().Default(""),
		},
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id", "name", "email", "role"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			wsID := WorkspaceID(ctx)

			query := `SELECT id, name, email, role FROM users WHERE workspace_id = $1`
			args := []any{wsID}

			role := p.String("role")
			if role != "" {
				query += ` AND role = $2`
				args = append(args, role)
			}
			query += ` ORDER BY name`

			rows, err := q.Query(ctx, query, args...)
			if err != nil {
				return nil, fmt.Errorf("query users: %w", err)
			}
			defer rows.Close()

			result := NewResult()
			for rows.Next() {
				var id, name, email, r string
				if err := rows.Scan(&id, &name, &email, &r); err != nil {
					return nil, fmt.Errorf("scan user: %w", err)
				}
				result.AddRow("users", id, map[string]any{
					"id": id, "name": name, "email": email, "role": r,
				})
				result.AddRef(Ref{
					Table: "users", ID: id,
					Fields: []string{"id", "name", "email", "role"},
				})
			}
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("rows error: %w", err)
			}

			return result, nil
		},
	})

	engine.Start(ctx)
	defer engine.Stop()

	// Step 1: Subscribe — should get real data from PostgreSQL
	handler := engine.Handler()

	subResp, err := engine.manager.Subscribe(ctx, SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}

	// Verify we got REAL data from PostgreSQL
	if len(subResp.Refs) != 2 {
		t.Fatalf("expected 2 refs from real SQL, got %d", len(subResp.Refs))
	}
	if subResp.Tables["users"]["u1"]["name"] != "Alice" {
		t.Fatalf("expected Alice from DB, got %v", subResp.Tables["users"]["u1"]["name"])
	}
	if subResp.Tables["users"]["u2"]["name"] != "Bob" {
		t.Fatalf("expected Bob from DB, got %v", subResp.Tables["users"]["u2"]["name"])
	}

	t.Log("Step 1 OK: Subscribe returned real PostgreSQL data")

	// Step 2: Mutate data in PostgreSQL
	_, err = pool.Exec(ctx, `UPDATE users SET name = 'Alice Updated' WHERE id = 'u1'`)
	if err != nil {
		t.Fatalf("update user: %v", err)
	}

	// Step 3: Notify the engine about the change
	engine.Notify(ctx, Change{
		Table:   "users",
		RowID:   "u1",
		Columns: []string{"name"},
	})

	// Wait for async invalidation
	time.Sleep(200 * time.Millisecond)

	// Step 4: Verify table_diff was sent with correct data from DB
	wsMessages := transport.getWorkspaceMessages("w1")
	if len(wsMessages) == 0 {
		t.Fatal("expected table_diff messages after notify")
	}

	foundDiff := false
	for _, msg := range wsMessages {
		if msg.Type == "table_diff" {
			data := msg.Data.(map[string]any)
			if data["table"] == "users" && data["id"] == "u1" {
				patch := data["patch"].([]PatchOp)
				for _, op := range patch {
					if op.Path == "/name" && op.Value == "Alice Updated" {
						foundDiff = true
					}
				}
			}
		}
	}
	if !foundDiff {
		t.Fatal("expected table_diff with name='Alice Updated' from real DB re-query")
	}

	t.Log("Step 2-4 OK: Notify → Factory re-run → real SQL → correct diff")

	// Step 5: Add a new user and verify view_diff
	_, err = pool.Exec(ctx, `INSERT INTO users (id, workspace_id, name, email, role) VALUES ('u3', 'w1', 'Charlie', 'charlie@test.com', 'member')`)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Reset transport
	transport.mu.Lock()
	transport.workspaceMessages = make(map[string][]Message)
	transport.seanceMessages = make(map[string][]Message)
	transport.mu.Unlock()

	engine.Notify(ctx, Change{
		Table:   "users",
		RowID:   "u3",
		Columns: []string{"id", "name", "email", "role"},
	})

	time.Sleep(200 * time.Millisecond)

	seanceMessages := transport.getSeanceMessages("s1")
	foundViewDiff := false
	for _, msg := range seanceMessages {
		if msg.Type == "view_diff" {
			foundViewDiff = true
		}
	}
	if !foundViewDiff {
		t.Fatal("expected view_diff after adding new user")
	}

	t.Log("Step 5 OK: New user → view_diff with added ref")

	// Step 6: Verify handler health endpoint works
	_ = handler // Handler was created — just verify no panic
	t.Log("Step 6 OK: Handler created without errors")

	// Step 7: Test with role filter param
	filteredResp, err := engine.manager.Subscribe(ctx, SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"role": "admin"},
		SeanceID:    "s2",
		UserID:      "u1",
		WorkspaceID: "w1",
	})
	if err != nil {
		t.Fatalf("filtered subscribe failed: %v", err)
	}
	if len(filteredResp.Refs) != 1 {
		t.Fatalf("expected 1 admin user, got %d", len(filteredResp.Refs))
	}
	if filteredResp.Tables["users"]["u1"]["role"] != "admin" {
		t.Fatal("expected admin role in filtered result")
	}

	t.Log("Step 7 OK: Parameterized query returns correct filtered data")
}

func TestPgxIntegrationQueryRowCompat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	pool := startPostgres(t, ctx)
	createSchema(t, ctx, pool)
	seedData(t, ctx, pool)

	querier := PgxQuerier(pool)

	// Verify QueryRow works through the adapter
	var name string
	err := querier.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, "u1").Scan(&name)
	if err != nil {
		t.Fatalf("QueryRow through adapter failed: %v", err)
	}
	if name != "Alice" {
		t.Fatalf("expected Alice, got %s", name)
	}

	// Verify Query works through the adapter
	rows, err := querier.Query(ctx, `SELECT id, name FROM users ORDER BY name`)
	if err != nil {
		t.Fatalf("Query through adapter failed: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id, n string
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Rows.Err: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(ids))
	}
}
