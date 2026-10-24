// Compile-only contracts. Runtime qualification lives in consumer.mjs.
import {
  ArcanaClient, ArcanaWSTransport, MemoryStorageAdapter, IndexedDBStorageAdapter,
  isViewTransportClient,
  type ArcanaConfig, type ArcanaSnapshot, type StorageAdapter, type ViewHandle,
  type ConnectionStatus, type OnUpdateCallback, type StatusCallback,
  type TransportClient, type ViewTransportClient, type WSTransportConfig,
  type Ref, type PatchOp, type TableDiff, type ViewDiff, type TransportMessage,
  type SubscribeResponse, type MutateResponse, type ApiResponse, type ViewMap, type TableMap,
} from '@frankfmy/arcana-sdk';
import {
  createArcana, createSubscription, type SvelteSubscription, type SubscriptionSnapshot,
} from '@frankfmy/arcana-sdk/adapters/svelte';

const config: WSTransportConfig = { url: 'ws://localhost/arcana/ws', auth: async () => 'fixture' };
const ws: ViewTransportClient = new ArcanaWSTransport(config);
const base: TransportClient = {
  connect: async () => {}, disconnect() {},
  subscribe(_channel, handler) {
    const message: TransportMessage = { type: 'table_diff', data: {} };
    handler(message);
    return () => {};
  },
};
const api: ArcanaConfig = { apiUrl: '/arcana', transport: base, fetchFn: fetch };
const memory: StorageAdapter = new MemoryStorageAdapter();
const indexedDBStorage: StorageAdapter = new IndexedDBStorageAdapter('type-contract');
const client = new ArcanaClient({ ...api, storage: memory });
const update: OnUpdateCallback = () => {};
const onStatus: StatusCallback = (status: ConnectionStatus) => { status satisfies ConnectionStatus; };
const stop: () => void = client.onStatusChange(onStatus);
const connected: Promise<void> = client.connect('fixture-workspace');
const handle: Promise<ViewHandle<Ref[]>> = client.subscribe('items', { scope: 'a' }, { onUpdate: update });
const row: Record<string, unknown> | null = client.getRow('items', 'row');
const table: Map<string, Record<string, unknown>> | undefined = client.getTable('items');
const mutation: Promise<{ name: string } | undefined> = client.mutate<{ name: string }>('rename', { name: 'After' });
const patch: PatchOp = { op: 'replace', path: '/name', value: 'After' };
const ref: Ref = { table: 'items', id: 'row', fields: ['name', 'scope'] };
const tableDiff: TableDiff = { table: 'items', id: 'row', ver: 2, patch: [patch] };
const viewDiff: ViewDiff = { view: 'items', params_hash: 'fixture', version: 2, refs_patch: [], tables: {} };
const reply: SubscribeResponse = { params_hash: 'fixture', version: 1, refs: [ref], tables: {}, seance_id: 'fixture' };
const envelope: ApiResponse<SubscribeResponse> = { ok: true, data: reply };
const mutationReply: MutateResponse<{ name: string }> = { ok: true, data: { name: 'After' } };
const views: ViewMap = { items: { params: { scope: 'a' }, refs: [ref] } };
const tables: TableMap = { items: { name: 'After', scope: 'a' } };
const snapshot: ArcanaSnapshot = {
  workspaceId: 'fixture-workspace', timestamp: 1, tables: {},
  views: { fixture: { refs: [ref], version: 1, graphKey: 'items', params: { scope: 'a' } } },
};
const saved: Promise<void> = memory.save(snapshot);
const loaded: Promise<ArcanaSnapshot | null> = indexedDBStorage.load();
const cleared: Promise<void> = memory.clear();
const persisted: Promise<void> = client.persist();
if (isViewTransportClient(base)) {
  const response: Promise<SubscribeResponse> = base.subscribeView('items', { scope: 'a' });
  const seance: string | null = base.seanceId;
}
const svelteClient: ArcanaClient = createArcana({ ...api, transport: ws, storage: memory });
const store: SvelteSubscription = createSubscription(svelteClient, 'items', { scope: 'a' });
const unsubscribe: () => void = store.subscribe((value: SubscriptionSnapshot) => {
  const refs: Ref[] = value.data;
  const version: number = value.version;
  const loading: boolean = value.loading;
});

// @ts-expect-error Graph keys are strings, not numeric identifiers.
client.subscribe(123, { scope: 'a' });
// @ts-expect-error Parameters are a record, not a serialized string.
client.subscribe('items', '{"scope":"a"}');
// @ts-expect-error Update callbacks receive no row argument.
client.subscribe('items', {}, { onUpdate: (_row: Ref) => {} });
// @ts-expect-error Row keys must be strings.
client.getRow('items', 123);
// @ts-expect-error Table keys must be strings.
client.getTable(123);
// @ts-expect-error Mutation parameters cannot be null under strict null checks.
client.mutate('rename', null);
// @ts-expect-error Status callbacks receive a ConnectionStatus, not a number.
client.onStatusChange((_status: number) => {});
// @ts-expect-error A transport connection must return a Promise<void>.
const invalidConnect: TransportClient['connect'] = () => undefined;
// @ts-expect-error Transport subscriptions must return a cleanup function.
const invalidSubscribe: TransportClient['subscribe'] = () => 123;
// @ts-expect-error WebSocket auth returns a string or Promise<string>.
new ArcanaWSTransport({ url: config.url, auth: () => 123 });
// @ts-expect-error Store subscribers receive snapshots, not strings.
store.subscribe((_snapshot: string) => {});
// @ts-expect-error The public patch protocol supports add/remove/replace only.
const invalidPatch: PatchOp = { op: 'move', path: '/name' };
