package config

import (
	"log/slog"
	"reflect"
	"strings"
)

// warnUnknownKeys compares the raw viper settings tree against the Config
// struct's mapstructure schema and logs a WARN for every yaml key that has no
// matching struct field. viper silently ignores unknown keys, so a typo like
// `route:` (instead of `routing:`) or a stale key left behind by an older
// release ("改了配置不生效" class of incidents) previously vanished without a
// trace — this makes them visible at load time.
//
// It is strictly observational: never returns an error, never mutates the
// config. A warning must not block startup (dynamic sections, env overlays and
// forward-compat keys all live in this config, so hard-failing would be too
// brittle). Dynamic maps (map[string]Struct) recurse into their value type
// instead of warning on the dynamic key itself.
func warnUnknownKeys(settings map[string]interface{}) {
	if len(settings) == 0 {
		return
	}
	unknown := findUnknownKeys(settings, reflect.TypeOf(Config{}), "")
	for _, path := range unknown {
		slog.Warn("unknown config key (ignored by gateway)", "key", path)
	}
	if len(unknown) > 0 {
		slog.Warn("config contains unknown keys — check for typos or removed options",
			"count", len(unknown), "hint", "keys are matched against the routing/config schema (mapstructure tags)")
	}
}

// findUnknownKeys walks a settings subtree against a struct type, returning
// dotted paths of unknown keys. prefix carries the parent path for messages.
func findUnknownKeys(settings map[string]interface{}, t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	fields := structFields(t)
	var unknown []string
	for key, val := range settings {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		spec, ok := fields[key]
		if !ok {
			unknown = append(unknown, path)
			continue
		}
		ft := spec
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			if sub, ok := val.(map[string]interface{}); ok {
				unknown = append(unknown, findUnknownKeys(sub, ft, path)...)
			}
		case ft.Kind() == reflect.Map:
			// Dynamic-map subtrees (backends.*, web_shell.modules.*) stay
			// opaque: their keys are user-defined AND often contain dots,
			// which viper treats as nesting separators in AllSettings() —
			// recursing would flag split artifacts like
			// "backends.kimi-k2.7-code" that exist only in viper's flattened
			// view, not in the yaml.
		}
		// Leaf kinds (primitives, slices, map[string]string, ...) are accepted
		// as-is: their children are either dynamic or out of schema's reach.
	}
	return unknown
}

// structFields maps lowercased mapstructure tags (viper lowercases yaml keys)
// to field types. Unnamed fields fall back to the lowercased field name.
func structFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name := strings.ToLower(f.Tag.Get("mapstructure"))
		if name == "" || name == "-" {
			name = strings.ToLower(f.Name)
		}
		if name == ",squash" || f.Anonymous {
			if f.Type.Kind() == reflect.Struct {
				for k, v := range structFields(f.Type) {
					fields[k] = v
				}
				continue
			}
		}
		fields[name] = f.Type
	}
	return fields
}

