package hardware

import (
    "errors"
    "os/exec"
    "strconv"
    "strings"
)

// vmStatOutputFn is the injectable seam for the `vm_stat` command output, so
// tests can feed canned Mach-VM text without exec'ing.
var vmStatOutputFn = defaultVMStatOutput

func defaultVMStatOutput() ([]byte, error) {
    return exec.Command("vm_stat").Output()
}

// readSwapPageCounts returns cumulative SWAP counters from the macOS Mach VM.
// History: the original source was `sysctl -n vm.pageins` / `vm.pageouts`, but
// those OIDs were removed on Darwin 25 (macOS 26) — `sysctl -n vm.pageins`
// returns "unknown oid" exit 1, which joined into CollectionError (EI4) and
// tripped the router's P0.5 collection_error_protection, diverting EVERY
// request (incl. local-exclusive models) to cloud. The first vm_stat-based fix
// parsed `Pageins:` / `Pageouts:` — but those are FILE-SYSTEM page counters
// (normal IO churn runs ~24k/s), not swap traffic, so the router's P2
// swap-thrash check tripped on ordinary disk IO and the threshold had to be
// pinned to a no-op value (#576). vm_stat also exposes the true swap counters
// `Swapins:` / `Swapouts:` on every Darwin release; parse those. The values
// are cumulative page counts; the collector diffs them per sample to derive a
// rate.
func readSwapPageCounts() (pageIn, pageOut uint64, err error) {
    out, err := vmStatOutputFn()
    if err != nil {
        return 0, 0, err
    }

    pageIn, okIn := parseVMStatLine(out, "Swapins:")
    pageOut, okOut := parseVMStatLine(out, "Swapouts:")
    if !okIn || !okOut {
        return 0, 0, errors.New("vm_stat output missing Swapins/Swapouts counters")
    }
    return pageIn, pageOut, nil
}

// parseVMStatLine scans vm_stat output for a label line (e.g. "Pageins:") and
// returns its trailing integer value. Lines are "Label:   <number>." — the
// value may be quoted ("Translation faults") so we take the last whitespace
// field and strip a trailing period.
func parseVMStatLine(output []byte, label string) (uint64, bool) {
    for _, line := range strings.Split(string(output), "\n") {
        if !strings.HasPrefix(line, label) {
            continue
        }
        fields := strings.Fields(line)
        if len(fields) < 2 {
            return 0, false
        }
        raw := strings.TrimSuffix(fields[len(fields)-1], ".")
        val, err := strconv.ParseUint(raw, 10, 64)
        if err != nil {
            return 0, false
        }
        return val, true
    }
    return 0, false
}
