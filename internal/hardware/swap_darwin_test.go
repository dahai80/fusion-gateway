//go:build darwin

package hardware

import (
    "testing"
)

// These tests exercise vm_stat plumbing that only exists on darwin
// (parseVMStatLine, vmStatOutputFn, readSwapPageCounts's vm_stat path).
// They were previously in the build-tag-neutral hardware_test.go, which made
// `GOOS=linux go vet ./...` (and `go test`) fail with "undefined:
// parseVMStatLine" — the darwin symbols are excluded from linux by the
// _darwin file-suffix constraint. Gated here so linux test compilation
// resolves (#147).

func TestReadSwapPageCounts(t *testing.T) {
    t.Log("testing readSwapPageCounts on darwin (vm_stat Swapins/Swapouts)")
    pageIn, pageOut, err := readSwapPageCounts()
    if err != nil {
        t.Fatalf("readSwapPageCounts failed on darwin (vm_stat should expose Swapins/Swapouts): %v", err)
    }
    t.Logf("pageIn=%d pageOut=%d", pageIn, pageOut)
}

func TestParseVMStatLine(t *testing.T) {
    t.Log("testing parseVMStatLine extracts trailing integer from vm_stat label lines")
    sample := []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
        "Pages free:   4211950.\n" +
        "\"Translation faults\":   1778725387.\n" +
        "Pageins:   98670869.\n" +
        "Pageouts:   26941.\n" +
        "Swapins:   777.\n" +
        "Swapouts:   42.\n")
    if in, ok := parseVMStatLine(sample, "Pageins:"); !ok || in != 98670869 {
        t.Errorf("Pageins: got (%d, %v), want (98670869, true)", in, ok)
    }
    if out, ok := parseVMStatLine(sample, "Pageouts:"); !ok || out != 26941 {
        t.Errorf("Pageouts: got (%d, %v), want (26941, true)", out, ok)
    }
    if in, ok := parseVMStatLine(sample, "Swapins:"); !ok || in != 777 {
        t.Errorf("Swapins: got (%d, %v), want (777, true)", in, ok)
    }
    if out, ok := parseVMStatLine(sample, "Swapouts:"); !ok || out != 42 {
        t.Errorf("Swapouts: got (%d, %v), want (42, true)", out, ok)
    }
    if _, ok := parseVMStatLine(sample, "Nonexistent:"); ok {
        t.Error("expected ok=false for absent label")
    }
}

func TestReadSwapPageCounts_MissingCounters(t *testing.T) {
    t.Log("testing readSwapPageCounts when vm_stat output lacks Swapouts")
    origFn := vmStatOutputFn
    vmStatOutputFn = func() ([]byte, error) {
        return []byte("Mach Virtual Memory Statistics:\nSwapins:   1000.\n"), nil
    }
    defer func() { vmStatOutputFn = origFn }()

    pageIn, pageOut, err := readSwapPageCounts()
    if err == nil {
        t.Fatal("expected error when vm_stat output missing Swapouts counter")
    }
    t.Logf("pageIn=%d, pageOut=%d, err=%v", pageIn, pageOut, err)
}

// #576: the counters must be the SWAP counters (Swapins/Swapouts), not the
// file-system page counters (Pageins/Pageouts). High Pageins + zero Swapins
// (sustained file IO on a healthy, non-swapping system) must read zero, or
// the router's P2 swap-thrash check trips on IO noise.
func TestReadSwapPageCounts_IgnoresFileSystemPageCounters(t *testing.T) {
    t.Log("testing readSwapPageCounts ignores Pageins/Pageouts (file-system counters)")
    origFn := vmStatOutputFn
    vmStatOutputFn = func() ([]byte, error) {
        return []byte("Pageins:   98670869.\nPageouts:   26941.\nSwapins:   0.\nSwapouts:   0.\n"), nil
    }
    defer func() { vmStatOutputFn = origFn }()

    pageIn, pageOut, err := readSwapPageCounts()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if pageIn != 0 || pageOut != 0 {
        t.Errorf("file-system churn leaked into swap counters: pageIn=%d pageOut=%d, want 0/0", pageIn, pageOut)
    }
}

func TestReadSwapPageCounts_Success(t *testing.T) {
    t.Log("testing readSwapPageCounts success path via vm_stat mock")
    origFn := vmStatOutputFn
    vmStatOutputFn = func() ([]byte, error) {
        return []byte("Pageins:   5000.\nPageouts:   3000.\nSwapins:   5000.\nSwapouts:   3000.\n"), nil
    }
    defer func() { vmStatOutputFn = origFn }()

    pageIn, pageOut, err := readSwapPageCounts()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if pageIn != 5000 {
        t.Errorf("pageIn: got %d, want 5000", pageIn)
    }
    if pageOut != 3000 {
        t.Errorf("pageOut: got %d, want 3000", pageOut)
    }
}
