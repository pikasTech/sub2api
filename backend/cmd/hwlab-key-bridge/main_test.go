package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
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

func TestStaleRouteRetiresOnlyItself(t *testing.T) {
	notFound := &ent.NotFoundError{}
	retire, fatal := routeRetirable(notFound, "", "")
	if !retire || fatal {
		t.Fatalf("soft-deleted group must retire only its route: retire=%v fatal=%v", retire, fatal)
	}
	if retire, fatal := routeRetirable(errors.New("db down"), "", ""); retire || !fatal {
		t.Fatalf("transient lookup failure must stay fatal: retire=%v fatal=%v", retire, fatal)
	}
	if retire, fatal := routeRetirable(nil, service.PlatformComposite, service.StatusActive); retire || fatal {
		t.Fatalf("active composite group must stay available: retire=%v fatal=%v", retire, fatal)
	}
	if retire, fatal := routeRetirable(nil, service.PlatformOpenAI, service.StatusDisabled); !retire || fatal {
		t.Fatalf("disabled group must retire only its route: retire=%v fatal=%v", retire, fatal)
	}
	if retire, fatal := routeRetirable(nil, "anthropic", service.StatusActive); !retire || fatal {
		t.Fatalf("platform-incompatible group must retire only its route: retire=%v fatal=%v", retire, fatal)
	}
}
