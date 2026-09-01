//go:build linux

package hardware

import "errors"

// readSwapPageCounts is the linux counterpart of swap_darwin.go. macOS exposes
// vm.pageins/vm.pageouts via sysctl; linux has no portable page-in/out counter
// (cgroup v2 memory.pressure is a different signal, and /proc/vmstat pgpgin/
// pgpgout are global, not per-container). Returning a tagged error — not zeros
// — lets collectSwapPageRate join it into CollectionError (EI4) so the router's
// P0.5 collection_error_protection sees the swap signal as unavailable rather
// than silently reading 0 (never trips). In the container topology (#143) the
// gateway is the HTTP business plane forwarding to bare-metal mlx; swap
// thrashing on the HOST (not the container) is what matters, and that is sensed
// by the bare-metal gateway, not the containerized one. So an unsupported error
// here is correct, not a gap.
func readSwapPageCounts() (pageIn, pageOut uint64, err error) {
    return 0, 0, errors.New("swap page counts unsupported on linux (no portable per-container counter; bare-metal gateway senses host thrashing)")
}
