package config

import (
    "encoding/json"
    "log/slog"
    "os"
    "reflect"
    "time"
)

// AuditEntry - one line per config change, JSONL format
// Called from WatchAndReload in config.go after globalConfig.Store(newSnap)
// Data schema: {timestamp, old_version, new_version, changes:[{field, old, new}]}
// User instruction: "继续phase 3" - implementing Task #22 config audit log from Phase 2 gap

type AuditEntry struct {
    Timestamp  string        `json:"timestamp"`
    OldVersion uint64        `json:"old_version"`
    NewVersion uint64        `json:"new_version"`
    Changes    []FieldChange `json:"changes"`
}

type FieldChange struct {
    Field string      `json:"field"`
    Old   interface{} `json:"old"`
    New   interface{} `json:"new"`
}

func AuditConfigChange(old, newSnap *ConfigSnapshot) {
    if !newSnap.Config.Observability.ConfigAuditLog {
        return
    }

    changes := diffConfigs(old.Config, newSnap.Config, "")
    if len(changes) == 0 {
        slog.Debug("config reload: no field-level changes detected")
        return
    }

    entry := AuditEntry{
        Timestamp:  time.Now().Format(time.RFC3339),
        OldVersion: old.Version,
        NewVersion: newSnap.Version,
        Changes:    changes,
    }

    slog.Info("config audit: changes detected",
        "old_version", old.Version,
        "new_version", newSnap.Version,
        "change_count", len(changes),
    )
    for _, c := range changes {
        slog.Info("config changed", "field", c.Field, "old", c.Old, "new", c.New)
    }

    auditFile := newSnap.Config.Observability.ConfigAuditFile
    if auditFile == "" {
        return
    }

    data, err := json.Marshal(entry)
    if err != nil {
        slog.Error("audit log marshal failed", "error", err)
        return
    }

    f, err := os.OpenFile(auditFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        slog.Error("audit log open failed", "file", auditFile, "error", err)
        return
    }
    defer f.Close()

    if _, err := f.Write(append(data, '\n')); err != nil {
        slog.Error("audit log write failed", "file", auditFile, "error", err)
    }
}

func diffConfigs(old, new Config, prefix string) []FieldChange {
    var changes []FieldChange

    oldV := reflect.ValueOf(old)
    newV := reflect.ValueOf(new)
    typ := oldV.Type()

    for i := 0; i < oldV.NumField(); i++ {
        field := typ.Field(i)
        fieldPath := field.Name
        if prefix != "" {
            fieldPath = prefix + "." + field.Name
        }

        oldField := oldV.Field(i)
        newField := newV.Field(i)

        if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeOf(time.Duration(0)) {
            changes = append(changes, diffStructFields(oldField, newField, fieldPath)...)
            continue
        }

        if field.Type.Kind() == reflect.Map || field.Type.Kind() == reflect.Slice {
            if !reflect.DeepEqual(oldField.Interface(), newField.Interface()) {
                changes = append(changes, FieldChange{
                    Field: fieldPath,
                    Old:   oldField.Interface(),
                    New:   newField.Interface(),
                })
            }
            continue
        }

        if !reflect.DeepEqual(oldField.Interface(), newField.Interface()) {
            changes = append(changes, FieldChange{
                Field: fieldPath,
                Old:   oldField.Interface(),
                New:   newField.Interface(),
            })
        }
    }

    return changes
}

func diffStructFields(oldField, newField reflect.Value, prefix string) []FieldChange {
    var changes []FieldChange
    typ := oldField.Type()

    for i := 0; i < oldField.NumField(); i++ {
        field := typ.Field(i)
        fieldPath := prefix + "." + field.Name

        of := oldField.Field(i)
        nf := newField.Field(i)

        if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeOf(time.Duration(0)) {
            changes = append(changes, diffStructFields(of, nf, fieldPath)...)
            continue
        }

        if field.Type.Kind() == reflect.Map || field.Type.Kind() == reflect.Slice {
            if !reflect.DeepEqual(of.Interface(), nf.Interface()) {
                changes = append(changes, FieldChange{
                    Field: fieldPath,
                    Old:   of.Interface(),
                    New:   nf.Interface(),
                })
            }
            continue
        }

        if !reflect.DeepEqual(of.Interface(), nf.Interface()) {
            changes = append(changes, FieldChange{
                Field: fieldPath,
                Old:   of.Interface(),
                New:   nf.Interface(),
            })
        }
    }

    return changes
}
