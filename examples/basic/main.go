// Example: basic Arcana setup with explicit notify.
//
// This demonstrates the minimal setup:
// 1. Define a graph
// 2. Create and start an engine
// 3. Mount the HTTP handler
// 4. Notify on data changes
//
// For a real application, you'd use a real pgx pool and Centrifugo transport.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/FrankFMY/arcana"
)

// Define a graph that lists users in an organization.
var UserList = arcana.GraphDef{
	Key: "user_list",

	Params: arcana.ParamSchema{
		"org_id": arcana.ParamUUID().Required(),
		"limit":  arcana.ParamInt().Default(50),
	},

	Deps: []arcana.TableDep{
		{Table: "users", Columns: []string{"id", "name", "email", "role"}},
	},

	Factory: func(ctx context.Context, q arcana.Querier, p arcana.Params) (*arcana.Result, error) {
		wsID := arcana.WorkspaceID(ctx)
		orgID := p.UUID("org_id")

		// In a real app, this would be a SQL query:
		// rows, err := q.Query(ctx, "SELECT ... FROM users WHERE org_id = $1", orgID)
		_ = wsID
		_ = orgID

		result := arcana.NewResult()

		// Simulated data
		result.AddRow("users", "u1", map[string]any{
			"id": "u1", "name": "Alice", "email": "alice@example.com", "role": "admin",
		})
		result.AddRow("users", "u2", map[string]any{
			"id": "u2", "name": "Bob", "email": "bob@example.com", "role": "member",
		})

		result.AddRef(arcana.Ref{
			Table: "users", ID: "u1", Fields: []string{"id", "name", "email", "role"},
		})
		result.AddRef(arcana.Ref{
			Table: "users", ID: "u2", Fields: []string{"id", "name", "email", "role"},
		})

		return result, nil
	},
}

func main() {
	// In a real app: pgxPool, _ := pgxpool.New(ctx, connStr)
	// In a real app: transport := arcana.NewCentrifugoTransport(...)

	engine := arcana.New(arcana.Config{
		Pool:      nil, // Would be pgx pool in production
		Transport: &noopTransport{},
		AuthFunc: func(r *http.Request) (*arcana.Identity, error) {
			return &arcana.Identity{
				SeanceID:    r.Header.Get("X-Seance-ID"),
				UserID:      "user-1",
				WorkspaceID: "workspace-1",
				Role:        "admin",
			}, nil
		},
		GCInterval: 30 * time.Second,
	})

	if err := engine.Register(UserList); err != nil {
		log.Fatal(err)
	}

	if err := engine.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer engine.Stop()

	// Mount Arcana handler
	mux := http.NewServeMux()
	mux.Handle("/arcana/", http.StripPrefix("/arcana", engine.Handler()))

	// Your app routes
	mux.HandleFunc("POST /api/users/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		// ... update user in database ...

		// Notify Arcana about the change
		engine.NotifyTable(r.Context(), "users", id, []string{"name", "email"})

		w.WriteHeader(http.StatusOK)
	})

	fmt.Println("Server starting on :8080")
	fmt.Println("Arcana endpoints at /arcana/{subscribe,unsubscribe,sync,active,schema,health}")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// noopTransport is a placeholder for the example.
type noopTransport struct{}

func (t *noopTransport) SendToSeance(_ context.Context, _ string, _ arcana.Message) error {
	return nil
}
func (t *noopTransport) SendToWorkspace(_ context.Context, _ string, _ arcana.Message) error {
	return nil
}
func (t *noopTransport) DisconnectSeance(_ context.Context, _ string) error { return nil }
