package hardware

// readSwapPageCountsFn is the injectable seam used by tests to stub the
// page-counter source without spawning a process. It lives in this
// build-tag-neutral file (not swap_darwin.go / swap_linux.go) so the symbol
// resolves on BOTH platforms: the file-suffix build constraint on
// swap_darwin.go (_darwin) excludes it from linux builds, which previously
// left this variable undefined on linux and broke `GOOS=linux go build`
// (#147). Each platform file defines readSwapPageCounts behind its own
// implicit suffix constraint; this var binds to whichever is active.
var readSwapPageCountsFn = readSwapPageCounts
