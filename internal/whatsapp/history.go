package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"strconv"
)

type IncomingMessage struct {
	ID         string          `json:"cursor"`
	ProviderID string          `json:"id"`
	Payload    json.RawMessage `json:"payload"`
}

func (p PostgresEvents) History(ctx context.Context, account, to, before, after string) (map[string]any, error) {
	if !identifier.MatchString(to) || before != "" && after != "" {
		return nil, errors.New("invalid history parameters")
	}
	cursor := int64(0)
	value := before
	if after != "" {
		value = after
	}
	if value != "" {
		var err error
		cursor, err = strconv.ParseInt(value, 10, 64)
		if err != nil || cursor < 0 {
			return nil, errors.New("invalid history cursor")
		}
	}
	c, err := pgx.Connect(ctx, p.URL)
	if err != nil {
		return nil, err
	}
	defer c.Close(context.Background())
	comparison := ""
	order := "DESC"
	args := []any{account, to}
	if before != "" {
		comparison = " AND id<$3"
		args = append(args, cursor)
	} else if after != "" {
		comparison = " AND id>$3"
		order = "ASC"
		args = append(args, cursor)
	}
	rows, err := c.Query(ctx, "SELECT id::text,provider_id,payload FROM relay_whatsapp_incoming WHERE account=$1 AND coalesce(nullif(payload->>'group_id',''),sender)=$2"+comparison+" ORDER BY relay_whatsapp_incoming.id "+order+" LIMIT 51", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []IncomingMessage{}
	bytes := 0
	more := false
	for rows.Next() {
		var m IncomingMessage
		if err = rows.Scan(&m.ID, &m.ProviderID, &m.Payload); err != nil {
			return nil, err
		}
		if len(messages) == 50 || (bytes+len(m.Payload) > 524288 && len(messages) > 0) {
			more = true
			break
		}
		messages = append(messages, m)
		bytes += len(m.Payload)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if order == "DESC" {
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}
	first, last := "", ""
	if len(messages) > 0 {
		first = messages[0].ID
		last = messages[len(messages)-1].ID
	}
	return map[string]any{"messages": messages, "first": first, "last": last, "has_more": more}, nil
}
func (s *Service) history(ctx context.Context, a Account, in Input) (map[string]any, error) {
	store, ok := s.Events.(interface {
		History(context.Context, string, string, string, string) (map[string]any, error)
	})
	if !ok {
		return nil, errors.New("history unavailable")
	}
	result, err := store.History(ctx, a.ID, in.To, in.Before, in.After)
	if err != nil {
		return nil, errors.New("cannot read conversation history")
	}
	return result, nil
}
