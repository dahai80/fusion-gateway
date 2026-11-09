package main

import "testing"

// #582: deploys silently served a stale binary because the built artifact
// (bin/gateway) and the systemd ExecStart path (./fusion-gateway) diverged.
// versionString gives deploy scripts and the startup log one canonical
// identity string to assert against (journalctl now reveals a stale deploy on
// the first boot line; --version asserts the artifact before restart).

func TestVersionString582(t *testing.T) {
    got := versionString("v1.2.3", "abc1234")
    want := "fusion-gateway v1.2.3 (commit abc1234)"
    if got != want {
        t.Fatalf("versionString = %q, want %q", got, want)
    }
    dev := versionString("dev", "unknown")
    if dev != "fusion-gateway dev (commit unknown)" {
        t.Fatalf("dev build string = %q", dev)
    }
}
