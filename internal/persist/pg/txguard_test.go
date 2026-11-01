package pg

import (
    "context"
    "errors"
    "testing"

    "github.com/jackc/pgx/v5"
)

// fakeTx embeds the pgx.Tx interface so unimplemented methods panic loudly
// if the guard ever calls one it should not.
type fakeTx struct {
    pgx.Tx
    rolledBack int
}

func (f *fakeTx) Rollback(context.Context) error {
    f.rolledBack++
    return nil
}

// fakePool is the beginTxer seam: scripts the Begin results in order.
type fakePool struct {
    begins []beginResult
    calls  int
}

type beginResult struct {
    tx  pgx.Tx
    err error
}

func (f *fakePool) Begin(context.Context) (pgx.Tx, error) {
    if f.calls >= len(f.begins) {
        return nil, errors.New("unexpected extra Begin call")
    }
    r := f.begins[f.calls]
    f.calls++
    return r.tx, r.err
}

// Reproduces the 2026-11-01 prod panic: the #407 isolation path re-begins,
// that Begin fails (PG timing out under memory pressure), and the deferred
// Rollback must not dereference a nil tx.
func TestTxGuardRebeginFailureKeepsOldTx(t *testing.T) {
    first := &fakeTx{}
    pool := &fakePool{begins: []beginResult{
        {tx: first},
        {err: errors.New("context deadline exceeded")},
    }}
    g := &txGuard{ctx: context.Background()}
    if err := g.begin(pool); err != nil {
        t.Fatalf("first begin must succeed: %v", err)
    }
    err := g.rebegin(pool)
    if err == nil {
        t.Fatal("rebegin must surface the Begin error")
    }
    if g.tx == nil {
        t.Fatal("rebegin failure must keep the old tx, not leave nil — deferred rollback would panic")
    }
    if first.rolledBack != 1 {
        t.Fatalf("rebegin must roll back the old tx first, got %d rollbacks", first.rolledBack)
    }
    g.rollback() // the deferred call — must not panic
    g.rollback() // idempotent-safe: rolled-back tx again is a no-op error, ignored
}

func TestTxGuardRollbackNilTxSafe(t *testing.T) {
    g := &txGuard{ctx: context.Background()}
    g.rollback() // must not panic on nil tx
    if g.tx != nil {
        t.Fatal("tx must stay nil")
    }
}

func TestTxGuardBeginFailureLeavesNilButRollbackSafe(t *testing.T) {
    pool := &fakePool{begins: []beginResult{{err: errors.New("connect refused")}}}
    g := &txGuard{ctx: context.Background()}
    if err := g.begin(pool); err == nil {
        t.Fatal("begin must fail")
    }
    g.rollback() // must not panic
}
