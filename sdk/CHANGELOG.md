# Changelog

## 0.1.0

- First named ESM package under the owner scope, with core/React/Vue/Svelte export paths and declarations; preserve the original MIT license and separate Go server product.
- Publish correct Node ESM relative imports, optional React/Vue peers and a Svelte store-compatible adapter without a runtime peer dependency.
- Capture submitted JSON parameters before asynchronous work and retain their identity in workspace-bound offline snapshots. Ignore ambiguous legacy cache entries and reject rebinding one client to another workspace.
- Fence Vue subscription generations so delayed old results cannot overwrite a newer parameter selection. Preserve independent completed and pending same-parameter ownership, ignore older response state and prevent late responses restoring a disconnected client.
- Share one pending/active physical server subscription between independent handles; serialize an HTTP resubscribe behind confirmed removal of its predecessor.
- Restore live views after native WebSocket session loss, decode server snapshot/catch-up sync responses, and apply unordered server ref additions in target-index order.
- Add actual installed server/client/runtime/type, isolated optional-peer and real browser persistence consumers.
