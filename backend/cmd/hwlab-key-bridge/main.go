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
	GatewayURL        string  `yaml:"gatewayUrl"`
	Routes            []route `yaml:"routes"`
}
type input struct {
	SubjectID string `json:"subjectId"`
	Ensure    bool   `json:"ensure"`
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
	if cfg.DatabaseSourceRef == "" || cfg.GatewayURL == "" || len(cfg.Routes) != 2 {
		return errors.New("invalid bridge configuration")
	}
	if cfg.Routes[0].ID != "mixed" || cfg.Routes[1].ID != "full" || cfg.Routes[0].GroupID == cfg.Routes[1].GroupID {
		return errors.New("distinct mixed and full routes required")
	}
	var request input
	if err = json.NewDecoder(io.LimitReader(in, 65536)).Decode(&request); err != nil {
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
	for i := range cfg.Routes {
		operation = "group-read"
		r := &cfg.Routes[i]
		g, e := groups.GetByID(ctx, r.GroupID)
		if e != nil {
			return e
		}
		if g.Platform != "openai" || g.Status != service.StatusActive {
			return errors.New("route unavailable")
		}
		r.RateMultiplier = g.RateMultiplier
		if override, ok := overrides[g.ID]; ok {
			r.RateMultiplier = override
		}
		r.RateMultiplier *= g.PeakMultiplierAt(time.Now())
		if !request.Ensure {
			continue
		}
		legacyName := "hwlab-gpt-" + r.ID
		name := "(HWLAB自动创建)" + legacyName
		// Include soft-deleted keys: revocation must not cause silent reissuance.
		operation = "managed-key-read"
		matches, e := client.APIKey.Query().Where(apikey.UserIDEQ(userID), apikey.NameIn(name, legacyName)).Select(apikey.FieldName, apikey.FieldID, apikey.FieldKey, apikey.FieldStatus, apikey.FieldDeletedAt, apikey.FieldGroupID).All(ctx)
		if e != nil {
			return e
		}
		if len(matches) > 1 {
			return errors.New("duplicate managed keys")
		}
		if len(matches) == 1 {
			k := matches[0]
			if k.DeletedAt != nil || k.Status != service.StatusActive || k.GroupID == nil || *k.GroupID != g.ID {
				return errors.New("managed key revoked")
			}
			if k.Name != name {
				operation = "managed-key-rename"
				if _, e := svc.Update(ctx, k.ID, userID, service.UpdateAPIKeyRequest{Name: &name}); e != nil {
					return e
				}
			}
			r.KeyID, r.Key, r.KeyName = k.ID, k.Key, name
		} else {
			operation = "managed-key-create"
			k, e := svc.Create(ctx, userID, service.CreateAPIKeyRequest{Name: name, GroupID: &g.ID})
			if e != nil {
				return e
			}
			r.KeyID, r.Key, r.KeyName = k.ID, k.Key, name
		}
	}
	return json.NewEncoder(out).Encode(map[string]any{"subjectId": request.SubjectID, "gatewayUrl": cfg.GatewayURL, "routes": cfg.Routes})
}
func readEnv(path string) (map[string]string, error) {
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
