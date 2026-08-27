# E8 — Engine.mu Per-Concern Mutex Split (Design)

Audit E8: "single `Engine.mu` serializes breakers + session affinity + hardware
metrics + local in-flight." Design splits into per-concern locks.

## Honest baseline (what the audit did NOT know)

The current code already mitigated E8's runtime premise before this design:

| Concern | Audit claim | Actual current code |
|---|---|---|
| Breaker `Trip` | blocks `Engine.mu.Lock` | `RLock`→find→`RUnlock`→`b.Trip()` lock-free (L1 fix) |
| Session affinity | serialized under `Engine.mu` | `sessionAffinity` has OWN `sync.RWMutex`; `RecordAffinity` takes NO `Engine.mu` |
| Hardware metrics | serialized under `Engine.mu` | `hwCollector` has OWN `sync.RWMutex`; `Engine.mu` guards only the pointer |
| local in-flight | serialized under `Engine.mu` | `atomic.CompareAndSwap` in adapter `tryInFlightAcquire` (RR4) |
| `Decide` hot path | blocked by writers | `decideLocked` runs under `RLock` (parallel readers); `cfg` read from context, not the mutex |

Field-reassignment audit (decides which locks are load-bearing):

- `sessionAffinity` — set ONCE in struct literal (engine.go:146), **never reassigned**.
- `hwCollector` — set ONCE (engine.go:139), **never reassigned**.
- `cfg` — swapped in 2 sites only: `UpdateConfig` + `DrainAndApply`. External callers: config-reload path (rare).
- `localReady`/`localInFlight`/`localModels`/`classifier`/`heuristicClassifier`/`adapterLookup`/`cluster` — swapped only in `Set*` wiring at startup (once), immutable after.
- `breakers`/`nodeBreakers` — the ONE genuinely contended write: `DrainAndApply` atomic rebuild (Phase 2 full-Lock) + lazy-create in `nodeBreakerLocked`.

So: **breakerMu is load-bearing. affinityMu + hardwareMu guard immutable pointers (ceremony, zero contention). inFlightMu absorbs ALL remaining rarely-written wiring.**

The user was told this and reaffirmed the full 4-way split anyway. This design honors that
choice but is honest about which locks are load-bearing vs ceremonial, so a reviewer is not
misled into thinking affinityMu/hardwareMu fix a real contention.

## Goal

Split `Engine.mu` into four concern-specific locks. Behavior identical (every existing
guard test passes unchanged). No new lock-ordering deadlock. `-race` clean on `./internal/router/`.

### Non-goals

- No change to routing decisions, breaker semantics, or config hot-reload behavior.
- No extraction of `Engine` into multiple structs (that is the god-file split, task #59).
- No touching `SessionAffinity`/`Collector`/adapter internals (they already self-lock).

## Design

### Four locks, named by the concern they protect

```
breakerMu   sync.RWMutex   // breakers + nodeBreakers (the ONLY contended write)
affinityMu  sync.RWMutex   // sessionAffinity pointer (immutable post-construction)
hardwareMu  sync.RWMutex   // hwCollector pointer (immutable post-construction)
inFlightMu  sync.RWMutex   // cfg + localReady + localInFlight + localModels +
                           // classifier + heuristicClassifier + adapterLookup +
                           // cluster + localQueue (rare writes, frequent Decide reads)
```

### Field ownership (one owner per field, no shared field)

| Field | Lock | Why this owner |
|---|---|---|
| `breakers`, `nodeBreakers` | `breakerMu` | only contended write (DrainAndApply rebuild + lazy node create) |
| `sessionAffinity` | `affinityMu` | dedicated concern; pointer immutable so this is a read-only guard |
| `hwCollector` | `hardwareMu` | dedicated concern; pointer immutable so this is a read-only guard |
| `cfg` | `inFlightMu` | swapped by UpdateConfig/DrainAndApply (rare) |
| `localReady` | `inFlightMu` | SetLocalReady wiring (rare) |
| `localInFlight` | `inFlightMu` | SetLocalInFlight wiring (rare) |
| `localModels` | `inFlightMu` | SetLocalModels wiring (rare) |
| `classifier` | `inFlightMu` | SetIntentClassifier (rare) |
| `heuristicClassifier` | `inFlightMu` | SetHeuristicClassifier (rare) |
| `adapterLookup` | `inFlightMu` | SetAdapterLookup (rare) |
| `cluster` | `inFlightMu` | SetClusterSelector (rare) |
| `localQueue` | `inFlightMu` | DrainAndApply RR6 rebuild (rare) |

One field, one owner. A field is never read under two different locks.

### Lock-ordering contract (deadlock prevention)

Single rule, enforced everywhere: **a goroutine holding `breakerMu` MUST NOT acquire
`inFlightMu` (or vice-versa).** `affinityMu`/`hardwareMu` are leaf locks (never held while
acquiring another).

- `Decide`: acquires `inFlightMu` (snapshot classifiers), releases, runs classification
  lock-free, then acquires `breakerMu` for `decideLocked`. The two never nest.
  → `decideLocked` reads breakers under `breakerMu`, reads localReady/localInFlight/
  localModels/classifier/hwCollector/sessionAffinity. The wiring fields move to `inFlightMu`,
  so `decideLocked` must snapshot them under `inFlightMu` FIRST, release, then take
  `breakerMu` for the breaker reads — OR snapshot everything needed in one `inFlightMu`
  pass and do breaker reads under `breakerMu`. (See "decideLocked restructure" below.)
- `DrainAndApply` Phase 2: the breaker rebuild needs `cfg` (to build new breakers) AND
  swaps `breakers`. This is the one place two locks could nest. Resolution: snapshot `cfg`
  under `inFlightMu` and RELEASE before taking `breakerMu` for the rebuild. `cfg` is
  passed in as a parameter to DrainAndApply already, so the snapshot is the param itself —
  no `inFlightMu` read needed for the breaker construction. Only the `e.cfg = cfg` store
  needs `inFlightMu`.
- `RecordNodeSuccess`/`RecordNodeFailure`: take `breakerMu` (lazy node create), release,
  then `b.RecordSuccess/Failure` lock-free. `cluster.MarkNodeBreaker*` called OUTSIDE any
  engine lock (as today). `e.cfg` read for `CircuitBreakerConfig` → snapshot under
  `inFlightMu` before the `breakerMu` window, or read once and pass in.

### decideLocked restructure (the hot path)

Today `decideLocked` does all reads under one RLock. After the split, the reads span two
concerns:

- breaker state (`breakers["local"].State()`, nodeBreakers) → `breakerMu`
- wiring reads (`localReady`, `localInFlight()`, `localModels()`, classifier pointers,
  `hwCollector.Latest()`, `sessionAffinity.Lookup()`) → `inFlightMu` / self-locked /
  `affinityMu`/`hardwareMu`

Restructure: `Decide` snapshots ALL wiring fields under a single `inFlightMu.RLock` into
a small `routeSnapshot` struct (the same pointer reads it does today), releases, then takes
`breakerMu.RLock` for `decideLocked` which now reads only breakers + the pre-snapshotted
wiring. Two sequential RLocks, never nested. The classifier snapshot already happens this
way (B1 fix); this generalizes it to all wiring fields.

```
type routeSnapshot struct {
    localReady      bool
    localInFlight   func() int64
    localModels     func() map[string]bool
    hwCollector     *hardware.Collector
    sessionAffinity *SessionAffinity
    classifier      IntentClassifier
    heuristic       IntentClassifier
    adapterLookup   AdapterLookup
    cluster         ClusterSelector
    localQueue      *slotQueue
}
```

`decideLocked(ctx, cfg, req, &trips, intent, snap)` — breaker reads under `breakerMu`,
wiring reads from `snap` (already captured). No lock nesting on the hot path.

### DrainAndApply (the one full-Lock window)

Phase 1 (drain): unchanged, lock-free polling of `localInFlight` (snapshot under
`inFlightMu` or call the already-wired func — it's a func callback, self-contained).

Phase 2 (apply): 
1. `inFlightMu.Lock()` → `e.cfg = cfg`; rebuild `localQueue` (reads cfg param, writes
   `e.localQueue`); `inFlightMu.Unlock()`.
2. `breakerMu.Lock()` → snapshot old breakers (inheritance, EI3), swap `breakers` +
   `nodeBreakers`, `InheritSnapshot` each; `breakerMu.Unlock()`.

Two separate Lock windows, never nested. `cfg` is the function parameter so breaker
construction needs no `inFlightMu` read. The RR6 queue-rebuild log compares old/new
queue state as today.

Phase 3 (warmup): `breakerMu.RLock` → `e.breakers["local"].ResetToHalfOpen()` → unlock.
Unchanged logic, new lock.

### Set\* methods (wiring)

Each `SetClusterSelector`/`SetIntentClassifier`/`SetHeuristicClassifier`/
`SetAdapterLookup`/`SetLocalReady`/`SetLocalInFlight`/`SetLocalModels`/`UpdateConfig`:
swap from `e.mu.Lock` to `inFlightMu.Lock`. Pure mechanical rename. `cfg`/classifier/etc
all own `inFlightMu` now.

### Record\*/Trip/NodeBreaker\* (breaker mutations)

- `RecordSuccess`/`RecordFailure`/`Trip`: `breakerMu.RLock`→find→release→`b.Record*`/`b.Trip`
  lock-free. Identical to today's L1 pattern, new lock name.
- `RecordNodeSuccess`/`RecordNodeFailure`: `breakerMu.Lock` (lazy node create) → release →
  `b.Record*` lock-free → `cluster.MarkNodeBreaker*` outside any lock. `e.cfg` for
  `CircuitBreakerConfig`: snapshot under `inFlightMu` BEFORE the `breakerMu` window (never
  nested), pass the config into `nodeBreakerLocked`.
- `NodeBreakerOpen`/`NodeBreakerState`/`CircuitBreakerState`: `breakerMu.RLock`→find→release
  →`b.State()`.
- `PublishBreakerStates`: `breakerMu.RLock`→collect backend/nodeID lists→release→loop
  `CircuitBreakerState`/`NodeBreakerState` (each re-acquires `breakerMu.RLock` briefly).
  Same pattern as today.

### affinityMu / hardwareMu (ceremonial — honest note)

`sessionAffinity` and `hwCollector` are never reassigned after construction. The locks
exist to honor the 4-way split the user chose and to future-proof against a later
reassignment (e.g. a hypothetical "reset session affinity" admin action). Today they guard
immutable pointers: `Shutdown` reads `sessionAffinity` under `affinityMu`; any
`hwCollector` read snapshots under `hardwareMu`. They add a per-read RLock cost on the hot
path for zero current contention — a known tradeoff the user accepted.

### RecordAffinity

Already takes NO `Engine.mu` — it nil-checks `sessionAffinity` and calls `sa.Record()`
(sa's own lock). After the split: nil-check `sessionAffinity` under `affinityMu.RLock`
(pointer read), release, then `sa.Record()`. Same external behavior.

## Lock-ordering summary

```
leaf locks (never nest):  affinityMu, hardwareMu
independent:              breakerMu, inFlightMu   (Decide acquires them SEQUENTIALLY, never nested)
DrainAndApply:            inFlightMu then breakerMu, SEPARATE windows (release between)
RecordNode*:              inFlightMu (cfg snapshot) THEN breakerMu, SEPARATE (release between)
```

No goroutine ever holds two of these locks at once. Deadlock surface = zero (no lock
ordering needed when no two are ever held simultaneously).

## Testing

- Every existing guard test passes unchanged (behavior identical).
- NEW guard `Test_E8_NoLockNesting`: a debug instrumentation build (or a test that wraps
  each lock with a "held set" tracker) asserts `Decide`/`DrainAndApply`/`RecordNode*` never
  hold `breakerMu` + `inFlightMu` simultaneously. Fail-on-bug.
- NEW guard `Test_E8_DecideTakesOnlyRLock`: asserts the hot path takes RLocks, never a
  full Lock, and that deferred trips fire outside any lock (mirrors the L1-fix guard).
- `-race` clean on `./internal/router/` (the package that exercises all lock sites).
- `go vet ./...` clean.

## Risk

HIGH (user-acknowledged). Hottest routing path. 4 locks where 1 sufficed. Mitigations:
sequential (never nested) acquisition eliminates lock-ordering bugs; field ownership is
one-owner-per-field (no ambiguity); decideLocked restructure is the largest change but
preserves the existing snapshot pattern (B1 already did this for classifiers). Revert path:
single `git revert`, the `Engine.mu` field + all call sites are mechanically traceable.

## Files touched

- `internal/router/engine.go` — struct (4 mutexes), all lock sites, decideLocked signature,
  routeSnapshot struct. ~90% of the change.
- `internal/router/engine_test.go` + new `internal/router/e8_mutex_split_test.go` — guards.
- No other package changes (Engine methods are the only public surface; callers unaffected
  since method signatures are unchanged except `decideLocked` which is unexported).
