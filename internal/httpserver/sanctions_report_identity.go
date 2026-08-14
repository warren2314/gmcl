package httpserver

import (
	"net/http"
	"strings"
)

type sanctionReportIdentity struct {
	Name            string
	Email           string
	Role            string
	ReportingClubID int32
	Authenticated   bool
}

func (s *Server) loadSanctionReportIdentity(r *http.Request) sanctionReportIdentity {
	if !strings.HasPrefix(r.URL.Path, "/captain/") {
		return sanctionReportIdentity{}
	}
	session, err := getCaptainSessionFromRequest(r)
	if err != nil || !s.captainSessionStillActive(r.Context(), session) {
		return sanctionReportIdentity{}
	}

	var identity sanctionReportIdentity
	if err := s.DB.QueryRow(r.Context(), `SELECT c.full_name,c.email,t.club_id
		FROM captains c JOIN teams t ON t.id=c.team_id
		WHERE c.id=$1 AND c.team_id=$2`, session.CaptainID, session.TeamID).
		Scan(&identity.Name, &identity.Email, &identity.ReportingClubID); err != nil {
		return sanctionReportIdentity{}
	}
	identity.Role = "Club captain"
	if session.SubmitterRole == "delegate" {
		identity.Role = "Captain's delegate"
		if value := strings.TrimSpace(session.SubmitterName); value != "" {
			identity.Name = value
		}
		if value := strings.TrimSpace(session.SubmitterEmail); value != "" {
			identity.Email = value
		}
	}
	identity.Authenticated = true
	return identity
}

func requireCaptainReportIdentity(w http.ResponseWriter, r *http.Request, identity sanctionReportIdentity) bool {
	if strings.HasPrefix(r.URL.Path, "/captain/") && !identity.Authenticated {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return false
	}
	return true
}
