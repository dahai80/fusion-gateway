//go:build !darwin

package hardware

import "errors"

// Non-darwin stubs: IOKit/CoreFoundation frameworks only exist on darwin.
// The real bindings live in iokit_purego_darwin.go behind the darwin build
// tag (purego.Dlopen has no windows build), so windows/linux builds need
// these symbols to resolve. collectIOKitGPU early-returns an unsupported
// error on non-darwin before reaching them, so these are compile-time
// shims, not behavior.
func initIOKit() {}

func collectIOKitGPUPurego(m *HardwareMetrics) (err error) {
    return errors.New("iokit gpu collection unsupported on non-darwin (no IOKit framework)")
}
