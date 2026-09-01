package hardware

import (
    "errors"
    "os/exec"
    "strconv"
    "strings"
)

// readSwapPageCountsFn is the injectable seam used by tests to stub the
// page-counter source without spawning a process. collector.go reassigns it on
// init; tests override it per-case.
var readSwapPageCountsFn = readSwapPageCounts

// vmStatOutputFn is the injectable seam for the `vm_stat` command output, so
// tests can feed canned Mach-VM text without exec'ing.
var vmStatOutputFn = defaultVMStatOutput

func defaultVMStatOutput() ([]byte, error) {
    return exec.Command("vm_stat").Output()
}

// readSwapPageCounts returns cumulative page-in/page-out counters from the
// macOS Mach VM. The historical source was `sysctl -n vm.pageins` /
// `vm.pageouts`, but those OIDs were removed on Darwin 25 (macOS 26) —
// `sysctl -n vm.pageins` returns "unknown oid" exit 1, which joined into
// CollectionError (EI4) and tripped the router's P0.5
// collection_error_protection, diverting EVERY request (incl. local-exclusive
// models) to cloud. `vm_stat` still exposes `Pageins:` / `Pageouts:` on every
// Darwin release, so parse that instead. The values are cumulative page counts;
// the collector diffs them per sample to derive a rate.
func readSwapPageCounts() (pageIn, pageOut uint64, err error) {
    out, err := vmStatOutputFn()
    if err != nil {
        return 0, 0, err
    }

    pageIn, okIn := parseVMStatLine(out, "Pageins:")
    pageOut, okOut := parseVMStatLine(out, "Pageouts:")
    if !okIn || !okOut {
        return 0, 0, errors.New("vm_stat output missing Pageins/Pageouts counters")
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
