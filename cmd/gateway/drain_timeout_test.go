package main

import (
	"testing"
	"time"
)

func TestShutdownDrainTimeout_HonorsConfiguredSeconds(t *testing.T) {
	if got := shutdownDrainTimeout(90); got != 90*time.Second {
		t.Fatalf("shutdownDrainTimeout(90) = %v, want 90s", got)
	}
}

func TestShutdownDrainTimeout_FallsBackWhenUnset(t *testing.T) {
	for _, v := range []int{0, -5} {
		if got := shutdownDrainTimeout(v); got != defaultShutdownDrainTimeout {
			t.Fatalf("shutdownDrainTimeout(%d) = %v, want %v", v, got, defaultShutdownDrainTimeout)
		}
	}
}

func TestShutdownDrainTimeout_DefaultIsThirtySeconds(t *testing.T) {
	if defaultShutdownDrainTimeout != 30*time.Second {
		t.Fatalf("defaultShutdownDrainTimeout = %v, want 30s", defaultShutdownDrainTimeout)
	}
}
