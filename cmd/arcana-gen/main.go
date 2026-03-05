// Command arcana-gen generates TypeScript type definitions from a
// registered Arcana graph registry. It produces tables.d.ts and views.d.ts.
//
// This is a template — users copy and modify it to import their own graph
// definitions. The generated files provide end-to-end type safety between
// Go factories and TypeScript client code.
//
// Usage:
//
//	go run ./cmd/arcana-gen -output ./sdk/generated/
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/FrankFMY/arcana"
)

func main() {
	outputDir := flag.String("output", "./sdk/generated", "Output directory for generated TypeScript files")
	flag.Parse()

	// Create a registry and register graphs.
	// In a real project, users import their graph definitions here.
	registry := arcana.NewRegistry()

	// Example: registry.Register(mygraphs.OrganizationLaborsList, mygraphs.OrdersList, ...)
	// For now, generate from an empty registry as a template.
	_ = registry

	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	// Generate tables.d.ts
	tablesPath := filepath.Join(*outputDir, "tables.d.ts")
	tablesFile, err := os.Create(tablesPath)
	if err != nil {
		log.Fatalf("create tables.d.ts: %v", err)
	}
	defer tablesFile.Close()

	if err := arcana.GenerateTables(tablesFile, registry); err != nil {
		log.Fatalf("generate tables: %v", err)
	}
	fmt.Printf("Generated %s\n", tablesPath)

	// Generate views.d.ts
	viewsPath := filepath.Join(*outputDir, "views.d.ts")
	viewsFile, err := os.Create(viewsPath)
	if err != nil {
		log.Fatalf("create views.d.ts: %v", err)
	}
	defer viewsFile.Close()

	if err := arcana.GenerateViews(viewsFile, registry); err != nil {
		log.Fatalf("generate views: %v", err)
	}
	fmt.Printf("Generated %s\n", viewsPath)
}
