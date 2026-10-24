# Security boundaries

The SDK talks to an authenticated Arcana Go server. Client state, workspace IDs and cached data do not grant permission. The application must enforce server authorization, credential refresh, origin/TLS policy and principal-specific storage. Never ship server credentials.

Use one client per workspace/principal, await initial connection and destroy subscription handles before discarding a client. JSON request parameters are captured at request entry. Offline snapshots require matching workspace and parameter identity; unidentified legacy data is not restored. These checks do not implement cache encryption, expiry, session revocation, trusted execution or an application authorization policy.

IndexedDB is an optional unencrypted browser cache. Its data can outlive login and remain readable to same-origin scripts. Partition it per principal/workspace, clear owned data only after pending work settles, and avoid persistence where that is unacceptable. Memory storage is the server-safe alternative, not durable storage.

React/Vue/Svelte adapters preserve application lifecycle; they are not an SSR authorization layer. Keep network connections and browser storage out of server rendering. Validate graph/action keys and parameters on the server. The test server is loopback-only, uses synthetic identities and an in-memory factory; it does not certify production authentication or PostgreSQL CDC.

Report sensitive issues privately to Pryanishnikovartem@gmail.com or frankfmy@proton.me; do not include real credentials or private cached data in an issue.
