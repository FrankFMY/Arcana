/** Serializable snapshot of the Arcana client state. */
export interface ArcanaSnapshot {
  tables: Record<string, Record<string, Record<string, unknown>>>;
  views: Record<
    string,
    {
      refs: import("./types").Ref[];
      version: number;
      graphKey: string;
    }
  >;
  timestamp: number;
}

/** Persistence adapter for offline storage. */
export interface StorageAdapter {
  save(data: ArcanaSnapshot): Promise<void>;
  load(): Promise<ArcanaSnapshot | null>;
  clear(): Promise<void>;
}

/** In-memory storage adapter (for SSR and tests). */
export class MemoryStorageAdapter implements StorageAdapter {
  private data: ArcanaSnapshot | null = null;

  async save(data: ArcanaSnapshot): Promise<void> {
    this.data = structuredClone(data);
  }

  async load(): Promise<ArcanaSnapshot | null> {
    return this.data ? structuredClone(this.data) : null;
  }

  async clear(): Promise<void> {
    this.data = null;
  }
}

/** IndexedDB storage adapter for browser offline persistence. */
export class IndexedDBStorageAdapter implements StorageAdapter {
  private dbName: string;
  private storeName = "arcana_state";

  constructor(dbName = "arcana") {
    this.dbName = dbName;
  }

  private open(): Promise<IDBDatabase> {
    return new Promise((resolve, reject) => {
      const req = indexedDB.open(this.dbName, 1);
      req.onupgradeneeded = () => {
        const db = req.result;
        if (!db.objectStoreNames.contains(this.storeName)) {
          db.createObjectStore(this.storeName);
        }
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  }

  async save(data: ArcanaSnapshot): Promise<void> {
    const db = await this.open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(this.storeName, "readwrite");
      const store = tx.objectStore(this.storeName);
      store.put(data, "snapshot");
      tx.oncomplete = () => {
        db.close();
        resolve();
      };
      tx.onerror = () => {
        db.close();
        reject(tx.error);
      };
    });
  }

  async load(): Promise<ArcanaSnapshot | null> {
    const db = await this.open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(this.storeName, "readonly");
      const store = tx.objectStore(this.storeName);
      const req = store.get("snapshot");
      req.onsuccess = () => {
        db.close();
        resolve(req.result ?? null);
      };
      req.onerror = () => {
        db.close();
        reject(req.error);
      };
    });
  }

  async clear(): Promise<void> {
    const db = await this.open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(this.storeName, "readwrite");
      const store = tx.objectStore(this.storeName);
      store.clear();
      tx.oncomplete = () => {
        db.close();
        resolve();
      };
      tx.onerror = () => {
        db.close();
        reject(tx.error);
      };
    });
  }
}
