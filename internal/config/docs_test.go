package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestDocsConfigurationCoversEveryKey keeps docs/configuration.md in step with
// the config structs: every YAML key path appears as `dotted.path`. A list of
// structs adds "[]" to its key (users.accounts[].name); maps are leaves.
func TestDocsConfigurationCoversEveryKey(t *testing.T) {
	data, err := os.ReadFile("../../docs/configuration.md")
	if err != nil {
		t.Fatalf("read docs/configuration.md: %v", err)
	}
	doc := string(data)
	paths := yamlKeyPaths(reflect.TypeOf(Config{}), "")
	if len(paths) < 50 {
		t.Fatalf("found only %d config keys; the walk is broken", len(paths))
	}
	for _, p := range paths {
		if !strings.Contains(doc, "`"+p+"`") {
			t.Errorf("docs/configuration.md does not document `%s`", p)
		}
	}
}

func yamlKeyPaths(typ reflect.Type, prefix string) []string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if strings.Contains(","+opts+",", ",inline,") {
			out = append(out, yamlKeyPaths(ft, prefix)...)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		path := prefix + name
		out = append(out, path)
		switch {
		case ft.Kind() == reflect.Struct:
			out = append(out, yamlKeyPaths(ft, path+".")...)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			out = append(out, yamlKeyPaths(ft.Elem(), path+"[].")...)
		}
	}
	return out
}
