package httpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (s *Server) audit(ctx context.Context, r *http.Request, actorType string, actorID *int32, action, entityType string, entityID *int64, metadata map[string]any) {
	if metadata == nil {
		metadata = make(map[string]any)
	}
	// attach request id for correlation, if present
	if rid := r.Header.Get("X-Request-ID"); rid != "" {
		metadata["request_id"] = rid
	}

	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		metaJSON = []byte("{}")
	}

	var ipVal any
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if parsed := net.ParseIP(host); parsed != nil {
			ipVal = parsed.String()
		}
	} else if parsed := net.ParseIP(r.RemoteAddr); parsed != nil {
		ipVal = parsed.String()
	}

	var aid any
	if actorID != nil {
		aid = *actorID
	}
	var eid any
	if entityID != nil {
		eid = *entityID
	}

	_, _ = s.DB.Exec(ctx, `
		INSERT INTO audit_logs (actor_type, actor_id, action, entity_type, entity_id, metadata, ip_address, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, actorType, aid, action, entityType, eid, metaJSON, ipVal, r.UserAgent())
}

// helper for scanning optional BIGINT IDs.
func scanBigInt(row pgx.Row, dest *int64, query string, args ...any) error {
	return row.Scan(dest)
}
