package main

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The adapter supports a gateway whose schema predates optional group fields.
// Required permission and billing fields are always selected. No migrations.
type bridgeGroups struct {
	service.GroupRepository
	client *ent.Client
	db     *sql.DB
}

func (r *bridgeGroups) GetByID(ctx context.Context, id int64) (*service.Group, error) {
	fields := []string{group.FieldID, group.FieldName, group.FieldPlatform, group.FieldRateMultiplier, group.FieldIsExclusive, group.FieldStatus, group.FieldSubscriptionType}
	var peak bool
	if err := r.db.QueryRowContext(ctx, `SELECT count(*)=4 FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name IN ('peak_rate_enabled','peak_start','peak_end','peak_rate_multiplier')`).Scan(&peak); err != nil {
		return nil, err
	}
	if peak {
		fields = append(fields, group.FieldPeakRateEnabled, group.FieldPeakStart, group.FieldPeakEnd, group.FieldPeakRateMultiplier)
	}
	g, err := r.client.Group.Query().Where(group.IDEQ(id), group.DeletedAtIsNil()).Select(fields...).Only(ctx)
	if err != nil {
		return nil, err
	}
	return &service.Group{ID: g.ID, Name: g.Name, Platform: g.Platform, RateMultiplier: g.RateMultiplier, IsExclusive: g.IsExclusive, Status: g.Status, SubscriptionType: g.SubscriptionType, PeakRateEnabled: g.PeakRateEnabled, PeakStart: g.PeakStart, PeakEnd: g.PeakEnd, PeakRateMultiplier: g.PeakRateMultiplier, Hydrated: true}, nil
}
