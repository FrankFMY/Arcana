# Contributing to Arcana

Thank you for your interest in contributing to Arcana.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/arcana.git`
3. Create a branch: `git checkout -b feature/your-feature`
4. Make your changes
5. Run tests: `go test -race ./... -count=1`
6. Push and open a Pull Request

## Development Setup

**Requirements:**
- Go 1.22+
- Docker (for integration tests)

**Running tests:**

```bash
# Unit tests only (no Docker needed)
go test -short ./...

# Full test suite including integration tests
go test -race ./... -count=1
```

Integration tests use [testcontainers-go](https://github.com/testcontainers/testcontainers-go) to spin up PostgreSQL and Centrifugo containers automatically.

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep code self-explanatory — avoid unnecessary comments
- Write tests for new functionality (TDD preferred)
- Keep changes focused — one feature or fix per PR

## Pull Request Process

1. Ensure all tests pass (`go test -race ./... -count=1`)
2. Ensure `go vet ./...` reports no issues
3. Update documentation if your change affects the public API
4. Keep commits atomic with clear messages

## Architecture Overview

- `Engine` is the main entry point — lifecycle management
- `Registry` stores graph definitions with an inverted table→graph index
- `Manager` handles subscription lifecycle and sync
- `Invalidator` processes changes: re-runs factories, computes diffs, dispatches transport
- `DataStore` is a 4-level normalized store (workspace → table → row) with RefCount GC
- `Transport` is a pluggable interface for message delivery (Centrifugo built-in)

## Reporting Issues

Open an issue on GitHub with:
- What you expected to happen
- What actually happened
- Steps to reproduce
- Go version and OS

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
