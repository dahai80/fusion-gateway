package cluster

import (
    "go/ast"
    "go/parser"
    "go/token"
    "testing"
)

// TestH5_DiscoveryLoopsUseJitteredTick: H5 (audit P1) — healthCheckLoop and
// masterSyncLoop must select on jitter.After(interval), NOT a fixed
// time.NewTicker. A fixed ticker makes every gateway in a cluster poll on the
// same interval edge → synchronized request spikes against shared upstreams.
// This guard parses discovery.go's AST and asserts each loop's select cases
// reference jitter.After. Revert a loop to time.NewTicker + ticker.C and the
// matching assertion fails (no jitter.After case found in that loop body).
func TestH5_DiscoveryLoopsUseJitteredTick(t *testing.T) {
    fset := token.NewFileSet()
    file, err := parser.ParseFile(fset, "discovery.go", nil, 0)
    if err != nil {
        t.Fatalf("H5: parse discovery.go: %v", err)
    }

    wantLoops := map[string]bool{
        "healthCheckLoop": false,
        "masterSyncLoop":  false,
    }

    ast.Inspect(file, func(n ast.Node) bool {
        fn, ok := n.(*ast.FuncDecl)
        if !ok || fn.Name == nil {
            return true
        }
        loopName := fn.Name.Name
        if _, tracked := wantLoops[loopName]; !tracked {
            return true
        }
        // Walk the function body for a CallExpr whose function is a SelectorExp
        // referencing jitter.After — that is the H5 de-synced wake.
        found := false
        ast.Inspect(fn, func(m ast.Node) bool {
            call, ok := m.(*ast.CallExpr)
            if !ok {
                return true
            }
            sel, ok := call.Fun.(*ast.SelectorExpr)
            if !ok {
                return true
            }
            ident, ok := sel.X.(*ast.Ident)
            if !ok {
                return true
            }
            if ident.Name == "jitter" && sel.Sel.Name == "After" {
                found = true
            }
            return true
        })
        wantLoops[loopName] = found
        return true
    })

    for loop, ok := range wantLoops {
        if !ok {
            t.Errorf("H5: %s does not select on jitter.After — reverted to a fixed time.NewTicker, the synchronized-herd vector is open", loop)
        }
    }
}
