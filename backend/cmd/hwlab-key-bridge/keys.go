package main

import (
	"context"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SPEC: PJ2026-01050206. The bridge only updates managed Key names.
// Avoid loading unrelated optional group columns from a newer gateway schema.
// Native Update still owns authorization and the exact changed-field mask.
type bridgeKeys struct {
	service.APIKeyRepository
	client *ent.Client
}

func (r *bridgeKeys) GetByID(ctx context.Context, id int64) (*service.APIKey, error) {
	k, err := r.client.APIKey.Query().Where(apikey.IDEQ(id), apikey.DeletedAtIsNil()).
		Select(apikey.FieldID, apikey.FieldUserID, apikey.FieldKey, apikey.FieldName, apikey.FieldStatus).Only(ctx)
	if err != nil {
		return nil, err
	}
	return &service.APIKey{ID: k.ID, UserID: k.UserID, Key: k.Key, Name: k.Name, Status: k.Status}, nil
}
