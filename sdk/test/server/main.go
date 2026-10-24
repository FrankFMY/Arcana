// Loopback protocol fixture with an in-memory data factory. It exercises the real
// engine/handlers, not PostgreSQL change capture or production authentication.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	arcana "github.com/FrankFMY/arcana"
)

type fixtureDB struct{}

func (fixtureDB) Query(context.Context, string, ...any) (arcana.Rows, error) {
	return nil, errors.New("fixture performs no SQL")
}
func (fixtureDB) QueryRow(context.Context, string, ...any) arcana.Row { return fixtureRow{} }

type fixtureRow struct{}

func (fixtureRow) Scan(...any) error { return errors.New("fixture performs no SQL") }

func main() {
	packageRoot := flag.String("package-root", "", "installed public package directory")
	flag.Parse()
	var session atomic.Int64
	identity := func(seance string) *arcana.Identity {
		return &arcana.Identity{SeanceID: seance, UserID: "fixture-user", WorkspaceID: "fixture-workspace"}
	}
	transport := arcana.NewWSTransport(arcana.WSTransportConfig{TokenAuthFunc: func(token string) (*arcana.Identity, error) {
		if token != "fixture" {
			return nil, errors.New("fixture auth rejected")
		}
		return identity(fmt.Sprintf("fixture-session-%d", session.Add(1))), nil
	}})
	engine := arcana.New(arcana.Config{Pool: fixtureDB{}, Transport: transport, InvalidationDebounce: time.Millisecond, AuthFunc: func(r *http.Request) (*arcana.Identity, error) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			return nil, errors.New("fixture auth rejected")
		}
		return identity(r.Header.Get("X-Fixture-Seance")), nil
	}})
	var mu sync.Mutex
	name := "Before"
	orderedCount := 0
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(engine.Register(arcana.GraphDef{Key: "items", Params: arcana.ParamSchema{"scope": arcana.ParamString().Default("a")}, Deps: []arcana.TableDep{{Table: "items", Columns: []string{"name"}}}, Factory: func(_ context.Context, _ arcana.Querier, p arcana.Params) (*arcana.Result, error) {
		mu.Lock()
		value := name
		mu.Unlock()
		result := arcana.NewResult()
		result.AddRow("items", "row", map[string]any{"name": value, "scope": p.String("scope")})
		result.AddRef(arcana.Ref{Table: "items", ID: "row", Fields: []string{"name", "scope"}})
		return result, nil
	}}))
	must(engine.RegisterMutation(arcana.MutationDef{Key: "rename", Params: arcana.ParamSchema{"name": arcana.ParamString().Required()}, Handler: func(_ context.Context, _ arcana.Querier, p arcana.Params) (*arcana.MutationResult, error) {
		mu.Lock()
		name = p.String("name")
		value := name
		mu.Unlock()
		return &arcana.MutationResult{Data: map[string]any{"name": value}, Changes: []arcana.Change{{Table: "items", RowID: "row", Columns: []string{"name"}}}}, nil
	}}))
	must(engine.Register(arcana.GraphDef{Key: "ordered", Deps: []arcana.TableDep{{Table: "items", Columns: []string{"name"}}}, Factory: func(_ context.Context, _ arcana.Querier, _ arcana.Params) (*arcana.Result, error) {
		mu.Lock()
		count := orderedCount
		mu.Unlock()
		result := arcana.NewResult()
		for i := 0; i < count; i++ {
			id := string(rune('a' + i))
			result.AddRow("ordered", id, map[string]any{"value": fmt.Sprintf("expanded-%d", count)})
			result.AddRef(arcana.Ref{Table: "ordered", ID: id, Fields: []string{"value"}})
		}
		return result, nil
	}}))
	must(engine.RegisterMutation(arcana.MutationDef{Key: "expand", Handler: func(_ context.Context, _ arcana.Querier, _ arcana.Params) (*arcana.MutationResult, error) {
		mu.Lock()
		orderedCount = 3
		mu.Unlock()
		return &arcana.MutationResult{Changes: []arcana.Change{{Table: "items", RowID: "row", Columns: []string{"name"}}}}, nil
	}}))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	must(engine.Start(ctx))
	defer engine.Stop()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Arcana package fixture</title><link rel="icon" href="data:,"></head><body><main id="app">Arcana package fixture</main></body></html>`)
	})
	mux.Handle("/arcana/ws", engine.WSHandler())
	mux.Handle("/arcana/", http.StripPrefix("/arcana", engine.Handler()))
	mux.HandleFunc("/fixture-stats", func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(engine.Stats()) })
	if *packageRoot != "" {
		mux.Handle("/package/", http.StripPrefix("/package/", http.FileServer(http.Dir(*packageRoot))))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()
	json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://" + listener.Addr().String()})
	<-ctx.Done()
	server.Close()
}
