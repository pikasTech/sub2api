// HWLAB native function adapter. Runs Sub2API's own key service, without
// changing the deployed gateway or its schema. Secret results use private pipes.
// SPEC: PJ2026-01050206.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"gopkg.in/yaml.v3"
)

type route struct {
	ID             string  `yaml:"id" json:"id"`
	Label          string  `yaml:"label" json:"label"`
	GroupID        int64   `yaml:"groupId" json:"groupId"`
	RateMultiplier float64 `json:"rateMultiplier"`
	KeyName        string  `json:"keyName,omitempty"`
	KeyID          int64   `json:"keyId,omitempty"`
	Key            string  `json:"apiKey,omitempty"`
}
type settings struct {
	DatabaseSourceRef string  `yaml:"databaseSourceRef"`
	DatabaseHost      string  `yaml:"databaseHost"`
	GatewayURL        string  `yaml:"gatewayUrl"`
	Routes            []route `yaml:"routes"`
}
type input struct {
	SubjectID string  `json:"subjectId"`
	Ensure    bool    `json:"ensure"`
	Routes    []route `json:"routes"`
}

var operation = "configuration"

func main() {
	path := flag.String("config", "", "owning YAML path")
	flag.Parse()
	if err := run(*path, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "sub2api-key-bridge: %s failed (%T); credential values suppressed\n", operation, err)
		var pg *pq.Error
		if errors.As(err, &pg) {
			fmt.Fprintf(os.Stderr, "database code=%s column=%s table=%s\n", pg.Code, pg.Column, pg.Table)
		}
		os.Exit(1)
	}
}
func run(path string, in io.Reader, out io.Writer) error {
	var cfg settings
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.DatabaseSourceRef == "" || cfg.GatewayURL == "" {
		return errors.New("invalid bridge configuration")
	}
	var request input
	if err = json.NewDecoder(io.LimitReader(in, 65536)).Decode(&request); err != nil {
		return err
	}
	// The caller injects the authoritative route table. The YAML list is only a
	// fallback for direct invocation, so adding a channel needs no redeploy.
	routes := request.Routes
	if len(routes) == 0 {
		routes = cfg.Routes
	}
	if err = validateRoutes(routes); err != nil {
		return err
	}
	userID, err := strconv.ParseInt(request.SubjectID, 10, 64)
	if err != nil || userID <= 0 {
		return errors.New("invalid synced user")
	}
	env, err := readEnv(cfg.DatabaseSourceRef)
	if err != nil {
		return err
	}
	if cfg.DatabaseHost != "" {
		env["DATABASE_HOST"] = cfg.DatabaseHost
	}
	port, err := strconv.Atoi(env["DATABASE_PORT"])
	if err != nil {
		return err
	}
	dbcfg := config.DatabaseConfig{Host: env["DATABASE_HOST"], Port: port, User: env["DATABASE_USER"], Password: env["DATABASE_PASSWORD"], DBName: env["DATABASE_DBNAME"], SSLMode: env["DATABASE_SSLMODE"]}
	db, err := sql.Open("postgres", dbcfg.DSN())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	users := repository.NewUserRepository(client, db)
	groups := &bridgeGroups{GroupRepository: repository.NewGroupRepository(client, db), client: client, db: db}
	keys := &bridgeKeys{APIKeyRepository: repository.NewAPIKeyRepository(client, db), client: client}
	rates := repository.NewUserGroupRateRepository(db)
	svc := service.NewAPIKeyService(keys, users, groups, repository.NewUserSubscriptionRepository(client), rates, nil, &config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	operation = "synced-user-read"
	user, err := users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if !user.IsActive() {
		return errors.New("user inactive")
	}
	operation = "rates-read"
	overrides, err := rates.GetByUserID(ctx, userID)
	if err != nil {
		return err
	}
	// Serialize the whole pair across concurrent bridge invocations. Key creation
	// itself remains the native service, including group permission validation.
	if request.Ensure {
		conn, e := db.Conn(ctx)
		if e != nil {
			return e
		}
		defer conn.Close()
		if _, e = conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext('sub2api-hwlab-keys'),$1)", userID); e != nil {
			return e
		}
		defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext('sub2api-hwlab-keys'),$1)", userID)
	}
	// A route that names a soft-deleted, disabled or platform-incompatible
	// group retires only itself. Dropping the whole catalog would turn one
	// stale row into a global 503 for every bound subject.
	available := make([]route, 0, len(routes))
	retired := make([]string, 0)
	for i := range routes {
		operation = "group-read"
		r := routes[i]
		g, e := groups.GetByID(ctx, r.GroupID)
		platform, status := "", ""
		if g != nil {
			platform, status = g.Platform, g.Status
		}
		if retire, fatal := routeRetirable(e, platform, status); retire {
			retired = append(retired, r.ID)
			continue
		} else if fatal {
			return e
		}
		r.RateMultiplier = g.RateMultiplier
		if override, ok := overrides[g.ID]; ok {
			r.RateMultiplier = override
		}
		r.RateMultiplier *= g.PeakMultiplierAt(time.Now())
		if request.Ensure {
			keyID, key, keyName, e := ensureRouteKey(ctx, client, svc, userID, g.ID, r.ID)
			if e != nil {
				return e
			}
			r.KeyID, r.Key, r.KeyName = keyID, key, keyName
		}
		available = append(available, r)
	}
	if len(available) == 0 {
		operation = "route-table"
		return errors.New("no available route")
	}
	return json.NewEncoder(out).Encode(map[string]any{"subjectId": request.SubjectID, "gatewayUrl": cfg.GatewayURL, "routes": available, "retiredRoutes": retired})
}

// routeRetirable keeps one stale route from taking down the whole catalog. A
// missing or soft-deleted group and a disabled or platform-incompatible group
// retire only that route; any other lookup failure stays fatal so a transient
// database error never silently degrades personal credentials.
func routeRetirable(err error, platform, status string) (retire, fatal bool) {
	if err != nil {
		return ent.IsNotFound(err), !ent.IsNotFound(err)
	}
	if !routePlatformAllowed(platform) || status != service.StatusActive {
		return true, false
	}
	return false, false
}

var routeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// validateRoutes only checks shape and uniqueness. Which routes exist is owned
// by the runtime route table, never by an enumeration in this adapter.
func validateRoutes(routes []route) error {
	if len(routes) == 0 {
		return errors.New("at least one route is required")
	}
	seen := map[string]bool{}
	for _, r := range routes {
		if !routeIDPattern.MatchString(r.ID) {
			return errors.New("invalid route id")
		}
		if seen[r.ID] {
			return errors.New("duplicate route id")
		}
		seen[r.ID] = true
		if r.GroupID <= 0 {
			return errors.New("invalid route group")
		}
	}
	return nil
}

func routePlatformAllowed(platform string) bool {
	return platform == service.PlatformOpenAI || platform == service.PlatformComposite
}

// ensureRouteKey resolves the single managed key of one route. Legacy names
// stay matched so the rename branch never issues a second key, and a revoked or
// re-grouped key fails closed instead of being silently reissued.
func ensureRouteKey(ctx context.Context, client *ent.Client, svc *service.APIKeyService, userID, groupID int64, routeID string) (int64, string, string, error) {
	legacyName := "hwlab-" + routeID
	name := "(HWLAB自动创建)" + legacyName
	previousName := "(HWLAB自动创建)hwlab-gpt-" + routeID
	operation = "managed-key-read"
	matches, err := client.APIKey.Query().
		Where(apikey.UserIDEQ(userID), apikey.NameIn(name, legacyName, previousName, "hwlab-gpt-"+routeID)).
		Select(apikey.FieldName, apikey.FieldID, apikey.FieldKey, apikey.FieldStatus, apikey.FieldDeletedAt, apikey.FieldGroupID).
		All(ctx)
	if err != nil {
		return 0, "", "", err
	}
	if len(matches) > 1 {
		return 0, "", "", errors.New("duplicate managed keys")
	}
	if len(matches) == 1 {
		key := matches[0]
		if key.DeletedAt != nil || key.Status != service.StatusActive || key.GroupID == nil || *key.GroupID != groupID {
			return 0, "", "", errors.New("managed key revoked")
		}
		if key.Name != name {
			operation = "managed-key-rename"
			if _, err := svc.Update(ctx, key.ID, userID, service.UpdateAPIKeyRequest{Name: &name}); err != nil {
				return 0, "", "", err
			}
		}
		return key.ID, key.Key, name, nil
	}
	operation = "managed-key-create"
	created, err := svc.Create(ctx, userID, service.CreateAPIKeyRequest{Name: name, GroupID: &groupID})
	if err != nil {
		return 0, "", "", err
	}
	return created.ID, created.Key, name, nil
}

func readEnv(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		values := map[string]string{}
		for _, key := range []string{"DATABASE_HOST", "DATABASE_PORT", "DATABASE_USER", "DATABASE_PASSWORD", "DATABASE_DBNAME", "DATABASE_SSLMODE"} {
			raw, err := os.ReadFile(filepath.Join(path, key))
			if err != nil {
				return nil, err
			}
			values[key] = strings.TrimSpace(string(raw))
		}
		return values, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			values[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	return values, nil
}
