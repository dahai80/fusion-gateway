package admin

// #662 regression: a parallel e2e harness drove the LIVE gateway's admin
// config API and disabled the auth portal mid-day. admin.config_mutation_lock
// must refuse every config-file write (single choke point: atomicWriteFile)
// while the flag is set in the live snapshot, and leave the file untouched.

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

const lockTestYAML = `
server:
  host: "0.0.0.0"
  port: 11432
admin:
  config_mutation_lock: true
`

func Test662_ConfigMutationLockRefusesWrites(t *testing.T) {
    dir := t.TempDir()
    cfgPath := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(cfgPath, []byte(lockTestYAML), 0600); err != nil {
        t.Fatal(err)
    }
    if _, err := config.Reload(cfgPath); err != nil {
        t.Fatalf("reload with lock config failed: %v", err)
    }
    t.Cleanup(func() {
        os.Unsetenv("FUSION_MLX_API_KEY")
    })

    h := &Handler{configPath: cfgPath}
    _, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
        doc["poisoned"] = true
        return nil
    })
    if err == nil {
        t.Fatal("updateYAMLSection must fail while config_mutation_lock is set")
    }
    raw, rerr := os.ReadFile(cfgPath)
    if rerr != nil {
        t.Fatal(rerr)
    }
    if string(raw) != lockTestYAML {
        t.Fatalf("locked write must leave the file untouched; diff:\n%s", raw)
    }
}

func Test662_ConfigMutationLockUnlockedWritesSucceed(t *testing.T) {
    dir := t.TempDir()
    cfgPath := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(cfgPath, []byte("server:\n  port: 11432\n"), 0600); err != nil {
        t.Fatal(err)
    }
    if _, err := config.Reload(cfgPath); err != nil {
        t.Fatalf("reload failed: %v", err)
    }

    h := &Handler{configPath: cfgPath}
    if _, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
        getOrCreateSection(doc, "hot_reload")["debounce"] = 500
        return nil
    }); err != nil {
        t.Fatalf("write without lock must succeed: %v", err)
    }
}
