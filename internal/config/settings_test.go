package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// tag is a field's key in the file.
func tag(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	return name
}

// The table holds every key of File's tables, and no other, so that the
// template and the checks miss no setting.
func TestSettingsAreTheFilesKeys(t *testing.T) {
	var want, got []string
	// A struct in a table is a table of its own, as symbols.branch.
	var walk func(name string, typ reflect.Type)
	walk = func(name string, typ reflect.Type) {
		if !slices.ContainsFunc(tables, func(t table) bool { return t.Name == name }) {
			t.Errorf("[%s] is not in tables", name)
		}
		for k := range typ.Fields() {
			if k.Type.Kind() == reflect.Struct {
				walk(name+"."+tag(k), k.Type)
				continue
			}
			want = append(want, name+"."+tag(k))
		}
	}
	for f := range reflect.TypeFor[File]().Fields() {
		if f.Type.Kind() != reflect.Struct {
			continue // [keys], [[sections]] and [[actions]] are not in the table
		}
		walk(tag(f), f.Type)
	}
	for _, s := range settings {
		got = append(got, s.Key)
		if s.Doc == "" {
			t.Errorf("%s says nothing of what it does", s.Key)
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("the table has %v\nFile has %v", got, want)
	}
	// Each value is the setting's own: with every string and list set to
	// a value of its own, the table reads what File holds at its key.
	f, values := marked()
	for _, s := range settings {
		if !reflect.DeepEqual(s.Value(f), values[s.Key]) {
			t.Errorf("%s reads another setting: %v", s.Key, s.Value(f))
		}
	}
}

// marked is a File with every setting of the tables set to a value of its
// own, and those values by table.key.
func marked() (File, map[string]any) {
	var f File
	values := map[string]any{}
	var mark func(name string, v reflect.Value)
	mark = func(name string, v reflect.Value) {
		for kf, fv := range v.Fields() {
			key := name + "." + tag(kf)
			switch fv.Kind() {
			case reflect.Struct:
				mark(key, fv)
				continue
			case reflect.String:
				fv.SetString(key)
			case reflect.Slice:
				fv.Set(reflect.ValueOf([]string{key}))
			case reflect.Bool:
				fv.SetBool(true)
			}
			values[key] = fv.Interface()
		}
	}
	for tf, tv := range reflect.ValueOf(&f).Elem().Fields() {
		if tv.Kind() == reflect.Struct {
			mark(tag(tf), tv)
		}
	}
	return f, values
}
