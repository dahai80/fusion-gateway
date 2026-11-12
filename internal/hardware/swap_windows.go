//go:build windows

package hardware

import "errors"

// readSwapPageCounts is the windows counterpart of swap_darwin.go / swap_linux.go.
// Windows exposes paging counters via WMI (Win32_PerfFormattedData_Memory) or
// PDH, neither of which is reachable from the stdlib without cgo/COM — returning
// a tagged error (not zeros) lets collectSwapPageRate join it into
// CollectionError (EI4) so the router's P0.5 collection_error_protection sees
// the swap signal as unavailable rather than silently reading 0 (never trips).
// The windows binary is a packaging target (#647); hardware-aware routing
// remains a darwin/linux bare-metal capability.
func readSwapPageCounts() (pageIn, pageOut uint64, err error) {
    return 0, 0, errors.New("swap page counts unsupported on windows (no stdlib-accessible paging counter; use PDH/WMI out of band)")
}
