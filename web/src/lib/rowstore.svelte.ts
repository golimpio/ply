// A list fetched whole from the server (movie cards, episodes), held per library version and patched
// by the client's own watch and progress writes instead of refetched. Rows are never mutated: a patch
// replaces the row and the array, so a reader keyed on a row or on the list sees every change.
import { untrack } from 'svelte';

export interface Row {
  id: number;
}

export interface RowSource<T extends Row, E> {
  /** The library version the rows belong to (app.libraryVersion): a new one makes ensure() refetch. */
  version: () => number;
  fetch: () => Promise<T[]>;
  /** Fetched with the rows and replaced with them (the show list). Its errors fail the load: absorb them to make it optional. */
  extra?: () => Promise<E>;
}

export type Fields<T extends Row> = Partial<Omit<T, 'id'>>;

type Pending = { start: number };

export class RowStore<T extends Row, E = undefined> {
  #rows = $state.raw<T[]>([]);
  #extra = $state.raw<E | undefined>(undefined);
  #epoch = $state(0);
  #loaded = $state(false);
  #error = $state('');
  #at = new Map<number, number>(); // id → position in #rows; a patch moves no row

  #src: RowSource<T, E>;
  #fetched = -1; // the version of the rows held
  #inflight: { version: number; promise: Promise<void> } | null = null;

  // The write log: every patch made while a fetch is in flight, so that the rows it brings back get
  // the writes made after it started. #pending is in start order.
  #seq = 0;
  #log: { seq: number; changes: [number, Fields<T>][] }[] = [];
  #pending = new Set<Pending>();

  constructor(src: RowSource<T, E>) {
    this.#src = src;
  }

  get rows(): readonly T[] {
    return this.#rows;
  }
  get extra(): E | undefined {
    return this.#extra;
  }
  /** Moves on when a list arrives, not on a patch: key what is "since the list was fetched" on it. */
  get epoch(): number {
    return this.#epoch;
  }
  get loaded(): boolean {
    return this.#loaded;
  }
  get error(): string {
    return this.#error;
  }
  /** Writes kept for the fetches in flight (for tests). */
  get logged(): number {
    return this.#log.length;
  }

  get(id: number): T | undefined {
    const rows = this.#rows; // read first: a reader that finds nothing yet must still hear the list land
    const i = this.#at.get(id);
    return i === undefined ? undefined : rows[i];
  }

  /**
   * Load (or refresh after a scan) the list. Safe to call often. A fetch started before the version
   * changed is not reused for the newer one, and an older answer landing late never overwrites a
   * newer one.
   */
  ensure(): Promise<void> {
    const version = this.#src.version();
    if (this.#fetched === version && this.#loaded) return Promise.resolve();
    if (this.#inflight?.version === version) return this.#inflight.promise;
    const f: Pending = { start: this.#seq };
    this.#pending.add(f);
    // In an executor, so a fetch that throws instead of rejecting still settles (and leaves #pending).
    const promise: Promise<void> = new Promise<[T[], E | undefined]>((resolve) => resolve(Promise.all([this.#src.fetch(), this.#src.extra?.()])))
      .then(([rows, extra]) => {
        if (version < this.#fetched) return; // a newer fetch already landed
        this.#land(rows, extra, f);
        this.#fetched = version;
      })
      .catch((e: unknown) => {
        if (version >= this.#fetched) this.#error = e instanceof Error ? e.message : String(e);
      })
      .finally(() => {
        if (this.#inflight?.promise === promise) this.#inflight = null;
        this.#pending.delete(f);
        this.#prune();
      });
    this.#inflight = { version, promise };
    return promise;
  }

  #land(fetched: T[], extra: E | undefined, f: Pending): void {
    const at = new Map<number, number>();
    fetched.forEach((r, i) => at.set(r.id, i));
    let rows = fetched;
    for (const w of this.#log) {
      if (w.seq <= f.start) continue; // the server had it when this list was read
      if (rows === fetched) rows = fetched.slice(); // the fetched array may be a cached answer
      for (const [id, fields] of w.changes) {
        const i = at.get(id);
        if (i !== undefined) rows[i] = { ...rows[i], ...fields };
      }
    }
    this.#rows = rows;
    this.#at = at;
    this.#extra = extra;
    this.#error = '';
    this.#loaded = true;
    this.#epoch++;
    // Fetches started before this one will be discarded when they land: they need no log.
    for (const p of this.#pending) {
      if (p === f) break;
      this.#pending.delete(p);
    }
  }

  // Keep only the writes a fetch still in flight has not seen.
  #prune(): void {
    const first = this.#pending.values().next().value;
    this.#log = first ? this.#log.filter((w) => w.seq > first.start) : [];
  }

  /** Replace a row with a copy carrying `fields`. Returns the new row, if the list has it. */
  patch(id: number, fields: Fields<T>): T | undefined {
    return this.patchAll([[id, fields]])[0];
  }

  /**
   * Patch several rows with one new array. A list still loading gets the same changes when it lands,
   * rows the current list lacks included. Returns the new rows the list has.
   */
  patchAll(changes: [number, Fields<T>][]): T[] {
    const seq = ++this.#seq;
    if (this.#pending.size) this.#log.push({ seq, changes });
    const rows = untrack(() => this.#rows).slice();
    const out: T[] = [];
    for (const [id, fields] of changes) {
      const i = this.#at.get(id);
      if (i === undefined) continue;
      rows[i] = { ...rows[i], ...fields };
      out.push(rows[i]);
    }
    if (out.length) this.#rows = rows;
    return out;
  }
}
