package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectedDatabaseSourceMatchesNativeSource(t *testing.T) {
	dir := t.TempDir()
	fields := map[string]string{"DATABASE_HOST": "127.0.0.1", "DATABASE_PORT": "55432", "DATABASE_USER": "native", "DATABASE_PASSWORD": "value=with=equals", "DATABASE_DBNAME": "sub2api", "DATABASE_SSLMODE": "disable"}
	var native string
	for key, value := range fields {
		if err := os.WriteFile(filepath.Join(dir, key), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		native += key + "=" + value + "\n"
	}
	file := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(file, []byte(native), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{file, dir} {
		got, err := readEnv(source)
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range fields {
			if got[key] != value {
				t.Fatalf("source field %s differs", key)
			}
		}
	}
	if err := os.Remove(filepath.Join(dir, "DATABASE_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnv(dir); err == nil {
		t.Fatal("missing projected secret must fail")
	}
}
