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
	for f := range reflect.TypeFor[File]().Fields() {
		if f.Type.Kind() != reflect.Struct {
			continue // [keys] and [[actions]] are not in the table
		}
		if !slices.ContainsFunc(tables, func(t table) bool { return t.Name == tag(f) }) {
			t.Errorf("[%s] is not in tables", tag(f))
		}
		for k := range f.Type.Fields() {
			want = append(want, tag(f)+"."+tag(k))
		}
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
	for tf, tv := range reflect.ValueOf(&f).Elem().Fields() {
		if tv.Kind() != reflect.Struct {
			continue
		}
		for kf, fv := range tv.Fields() {
			key := tag(tf) + "." + tag(kf)
			switch fv.Kind() {
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
	return f, values
}
