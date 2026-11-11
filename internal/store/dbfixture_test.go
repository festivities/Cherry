package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// fillSynthetic sets every exported field of v, recursively, to a non-zero value: strings "s", numbers 7, booleans true,
// one element in every slice and map. A round trip of a filled value then covers every persisted field of its type, and
// a new field that is not stored shows up as a difference.
func fillSynthetic(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillSynthetic(v.Field(i))
			}
		}
	case reflect.String:
		v.SetString("s")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillSynthetic(p.Elem())
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillSynthetic(s.Index(0))
		v.Set(s)
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fillSynthetic(v.Index(i))
		}
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillSynthetic(k)
		e := reflect.New(v.Type().Elem()).Elem()
		fillSynthetic(e)
		m.SetMapIndex(k, e)
		v.Set(m)
	default:
		panic(fmt.Sprintf("fillSynthetic: kind %s is not stored", v.Kind()))
	}
}

// openTestDB opens a fresh database in a temporary directory and returns it with its path.
func openTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), DBFileName)
	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

// syntheticAccount is a fully populated account (every stored field set) with its own access token, an alias and a guest
// code. i makes the aid, identity and guest code unique. The clear secrets are returned for the tests that look for them.
func syntheticAccount(i int) (acc *Account, own, alias, guest string) {
	var s StoredAccount
	fillSynthetic(reflect.ValueOf(&s).Elem())
	s.Aid = strconv.Itoa(1000 + i)
	s.Identity = "dev-" + strconv.Itoa(i)
	s.AccessToken = RandomToken(64)
	s.GuestCode = "guest-" + strconv.Itoa(i) + "-" + RandomToken(16)
	return accountFromStored(s), s.AccessToken, RandomToken(64), s.GuestCode
}

// checkRoundTrip writes want, reads it back and requires the same JSON.
func checkRoundTrip[T any](t *testing.T, d *DB, name string, want T, write func(*sql.Tx, T) error, read func(Querier) (T, error)) {
	t.Helper()
	if err := d.Update(func(tx *sql.Tx) error { return write(tx, want) }); err != nil {
		t.Fatalf("%s write: %v", name, err)
	}
	var got T
	if err := d.View(func(q Querier) error {
		var err error
		got, err = read(q)
		return err
	}); err != nil {
		t.Fatalf("%s read: %v", name, err)
	}
	if err := checkJSON(name, want, got); err != nil {
		t.Fatal(err)
	}
}
