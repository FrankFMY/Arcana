# Changelog

## v0.2.0

### Added

- **Built-in WebSocket transport** — zero-dependency real-time sync without Centrifugo or any external service. Two auth modes: HTTP upgrade (cookies/headers) and token-based (SPA/mobile). Request/reply correlation with `id`/`reply_to` for subscribe, unsubscribe, sync, mutate over a single WS connection. Configurable write buffer, ping/pong, and accept options.
- **Mutations** — server-side write operations registered with `Engine.RegisterMutation()`. Execute via HTTP `POST /mutate` or WebSocket `{"type":"mutate"}`. Supports typed parameter validation, and auto-notifies changes for reactive view invalidation. Identity available in handlers via `IdentityFromCtx(ctx)`.
- **React adapter** (`sdk/adapters/react.ts`) — `ArcanaProvider`, `useArcana`, `useView`, `useRow`, `useStatus`, `useMutation` hooks. Uses `useSyncExternalStore` for tear-free status, ref-box pattern for race-safe updates, and SSR-compatible server snapshot.
- **Vue 3 adapter** (`sdk/adapters/vue.ts`) — `provideArcana`, `useArcana`, `useView`, `useRow`, `useStatus`, `useMutation` composables. Uses `shallowRef` for data, `watch` with `{ immediate: true }` for subscriptions, and `onUnmounted` for cleanup.
- **DevTools panel** — `engine.DevToolsHandler()` returns an http.Handler serving a dark-themed dashboard at `/` and JSON state at `/state`. Displays graphs, mutations, schema, active subscriptions, WS connections, and data store rows with 2-second auto-refresh.
- **SDK WebSocket transport** (`sdk/transports/ws.ts`) — `ArcanaWSTransport` implementing `ViewTransportClient` for subscribe/unsubscribe/mutate via WS. Reconnect with exponential backoff, request timeout, and configurable delays.
- **SDK mutation support** — `ArcanaClient.mutate()` routes via WS `mutateView` or HTTP `apiCall` depending on transport type.
- **Dashboard example** (`examples/dashboard/`) — real-time dashboard with live metrics, task management via mutations, WS transport, and DevTools. Runnable with `go run ./examples/dashboard`.

### Fixed

- **Seance channel routing** — WS transport delivers all messages through a single pipe; SDK skips redundant seance channel subscription for WS transports.
- **Reconnect handler** — `scheduleReconnect()` sets callbacks before `openSocket()` to prevent race. SDK `handleReconnect()` re-subscribes workspace channel and conditionally re-subscribes seance channel.
- **React useView race condition** — `onUpdate` callback could fire before `.then()` resolved the handle. Fixed with ref-box pattern `{ handle: null }`.
- **React useStatus SSR** — added server snapshot `() => "disconnected"` as third argument to `useSyncExternalStore`.
- **React useMutation unmount safety** — guards `setLoading` calls with mounted check to prevent state updates on unmounted components.
- **Vue useRow reactivity** — added optional `trigger` parameter (version ref from useView) to re-read row data when underlying store updates.
- **DevTools mount path** — removed hardcoded `StripPrefix("/arcana/devtools")` that broke custom mount paths. Handler now returns raw mux; user controls prefix stripping.

### Breaking Changes

None. All v0.1.x APIs remain compatible. BizEngine compiles without changes (only needs `go get github.com/coder/websocket` for the new transitive dependency).
