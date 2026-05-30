package httpserver

import (
	"context"
	"crypto/sha256"
	"net/url"
	"strings"
	"time"
)

type magicLinkEventContext struct {
	TokenID   int64
	CaptainID int32
	TeamID    int32
	SeasonID  int32
	WeekID    int32
	MatchDate *time.Time
}

func (s *Server) magicTokenIDForPlaintext(ctx context.Context, token string) *int64 {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	hash := sha256.Sum256([]byte(token))
	var id int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM magic_link_tokens WHERE token_hash=$1`, hash[:]).Scan(&id); err != nil {
		return nil
	}
	return &id
}

func (s *Server) magicLinkContextForURL(ctx context.Context, rawLink string) (magicLinkEventContext, bool) {
	token := magicLinkTokenFromURL(rawLink)
	if token == "" {
		return magicLinkEventContext{}, false
	}
	hash := sha256.Sum256([]byte(token))

	var out magicLinkEventContext
	err := s.DB.QueryRow(ctx, `
		SELECT mlt.id, mlt.captain_id, c.team_id, mlt.season_id, mlt.week_id, mlt.match_date
		FROM magic_link_tokens mlt
		JOIN captains c ON c.id = mlt.captain_id
		WHERE mlt.token_hash = $1
	`, hash[:]).Scan(&out.TokenID, &out.CaptainID, &out.TeamID, &out.SeasonID, &out.WeekID, &out.MatchDate)
	return out, err == nil
}

func magicLinkTokenFromURL(rawLink string) string {
	rawLink = strings.TrimSpace(rawLink)
	if rawLink == "" {
		return ""
	}
	parsed, err := url.Parse(rawLink)
	if err != nil {
		return ""
	}
	if token := strings.TrimSpace(parsed.Query().Get("token")); token != "" {
		return token
	}
	for _, key := range []string{"url", "u", "redirect", "redirect_url"} {
		nested := strings.TrimSpace(parsed.Query().Get(key))
		if nested == "" {
			continue
		}
		if token := magicLinkTokenFromURL(nested); token != "" {
			return token
		}
	}
	return ""
}

func redactMagicTokenInText(text string) string {
	const marker = "token="
	var b strings.Builder
	offset := 0
	for {
		idx := strings.Index(text[offset:], marker)
		if idx < 0 {
			b.WriteString(text[offset:])
			return b.String()
		}
		idx += offset
		start := idx + len(marker)
		end := start
		for end < len(text) {
			switch text[end] {
			case '&', ' ', '\t', '\r', '\n', '"', '\'':
				goto replace
			default:
				end++
			}
		}
	replace:
		b.WriteString(text[offset:start])
		b.WriteString("[redacted]")
		offset = end
	}
}
