package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

type adminClubEdit struct {
	ID        int32
	Name      string
	ShortName string
}

type adminTeamEdit struct {
	ID                int32
	ClubID            int32
	Name              string
	Level             *int32
	Active            bool
	PlayCricketTeamID string
}

type adminCaptainEdit struct {
	ID         int32
	TeamID      int32
	FullName    string
	Email       string
	Phone       string
	ActiveFrom  string
	ActiveTo    string
	CurrentlyOn bool
}

type adminClubOption struct {
	ID   int32
	Name string
}

type adminTeamOption struct {
	ID          int32
	ClubID      int32
	DisplayName string
}

type adminTeamCaptainRow struct {
	TeamID            int32
	ClubID            int32
	ClubName          string
	TeamName          string
	Active            bool
	Level             *int32
	PlayCricketTeamID string
	CaptainID         *int32
	CaptainName       string
	CaptainEmail      string
	CaptainPhone      string
}

type adminCaptainRow struct {
	ID         int32
	ClubName   string
	TeamName   string
	FullName   string
	Email      string
	Phone      string
	ActiveFrom string
	ActiveTo   string
	IsActive   bool
}

type adminTeamsCaptainsPageData struct {
	Query         string
	Filter        string
	ClubEdit      adminClubEdit
	TeamEdit      adminTeamEdit
	CaptainEdit   adminCaptainEdit
	ClubOptions   []adminClubOption
	TeamOptions   []adminTeamOption
	TeamRows      []adminTeamCaptainRow
	CaptainRows   []adminCaptainRow
	ClubCount     int
	TeamCount     int
	ActiveTeams   int
	ActiveCaps    int
	MissingCaps   int
	SuccessMsg    string
	ErrorMsg      string
}

func (s *Server) handleAdminTeamsCaptainsGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		data, err := s.buildAdminTeamsCaptainsPageData(ctx, r)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.renderAdminTeamsCaptainsPage(w, r, data)
	}
}

func (s *Server) handleAdminClubSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("club_id")), 10, 32)
		name := strings.TrimSpace(r.FormValue("name"))
		shortName := strings.TrimSpace(r.FormValue("short_name"))
		if name == "" {
			s.redirectTeamsCaptainsError(w, r, "Club name is required.")
			return
		}

		var err error
		if id > 0 {
			_, err = s.DB.Exec(ctx, `
				UPDATE clubs
				SET name = $1, short_name = NULLIF($2, '')
				WHERE id = $3
			`, name, shortName, int32(id))
			if err == nil {
				s.audit(ctx, r, "admin", nil, "club_updated", "club", func() *int64 { v := int64(id); return &v }(), map[string]any{"name": name})
			}
		} else {
			var clubID int32
			err = s.DB.QueryRow(ctx, `
				INSERT INTO clubs (name, short_name)
				VALUES ($1, NULLIF($2, ''))
				RETURNING id
			`, name, shortName).Scan(&clubID)
			if err == nil {
				s.audit(ctx, r, "admin", nil, "club_created", "club", func() *int64 { v := int64(clubID); return &v }(), map[string]any{"name": name})
			}
		}
		if err != nil {
			s.redirectTeamsCaptainsError(w, r, "Could not save club: "+err.Error())
			return
		}
		s.redirectTeamsCaptainsSuccess(w, r, "Club saved.")
	}
}

func (s *Server) handleAdminTeamSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("team_id")), 10, 32)
		clubID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("club_id")), 10, 32)
		name := strings.TrimSpace(r.FormValue("name"))
		playCricketID := strings.TrimSpace(r.FormValue("play_cricket_team_id"))
		active := r.FormValue("active") == "on"
		levelRaw := strings.TrimSpace(r.FormValue("level"))
		var level *int32
		if levelRaw != "" {
			parsed, err := strconv.ParseInt(levelRaw, 10, 32)
			if err != nil {
				s.redirectTeamsCaptainsError(w, r, "Team level must be a number.")
				return
			}
			v := int32(parsed)
			level = &v
		}
		if clubID <= 0 || name == "" {
			s.redirectTeamsCaptainsError(w, r, "Team club and name are required.")
			return
		}

		var err error
		if id > 0 {
			_, err = s.DB.Exec(ctx, `
				UPDATE teams
				SET club_id = $1,
				    name = $2,
				    level = $3,
				    active = $4,
				    play_cricket_team_id = NULLIF($5, '')
				WHERE id = $6
			`, int32(clubID), name, level, active, playCricketID, int32(id))
			if err == nil {
				s.audit(ctx, r, "admin", nil, "team_updated", "team", func() *int64 { v := int64(id); return &v }(), map[string]any{"name": name})
			}
		} else {
			var teamID int32
			err = s.DB.QueryRow(ctx, `
				INSERT INTO teams (club_id, name, level, active, play_cricket_team_id)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''))
				RETURNING id
			`, int32(clubID), name, level, active, playCricketID).Scan(&teamID)
			if err == nil {
				s.audit(ctx, r, "admin", nil, "team_created", "team", func() *int64 { v := int64(teamID); return &v }(), map[string]any{"name": name})
			}
		}
		if err != nil {
			s.redirectTeamsCaptainsError(w, r, "Could not save team: "+err.Error())
			return
		}
		s.redirectTeamsCaptainsSuccess(w, r, "Team saved.")
	}
}

func (s *Server) handleAdminTeamToggleActive() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		teamID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 32)
		if teamID <= 0 {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_, err := s.DB.Exec(ctx, `UPDATE teams SET active = NOT active WHERE id = $1`, int32(teamID))
		if err != nil {
			s.redirectTeamsCaptainsError(w, r, "Could not update team status: "+err.Error())
			return
		}
		s.audit(ctx, r, "admin", nil, "team_toggled", "team", func() *int64 { v := teamID; return &v }(), map[string]any{})
		s.redirectTeamsCaptainsSuccess(w, r, "Team status updated.")
	}
}

func (s *Server) handleAdminCaptainSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("captain_id")), 10, 32)
		teamID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("team_id")), 10, 32)
		fullName := strings.TrimSpace(r.FormValue("full_name"))
		emailAddr := strings.TrimSpace(r.FormValue("email"))
		phone := strings.TrimSpace(r.FormValue("phone"))
		activeFrom := strings.TrimSpace(r.FormValue("active_from"))
		activeToRaw := strings.TrimSpace(r.FormValue("active_to"))
		activeNow := r.FormValue("currently_on") == "on"
		if teamID <= 0 || fullName == "" || emailAddr == "" || activeFrom == "" {
			s.redirectTeamsCaptainsError(w, r, "Captain team, name, email, and active-from date are required.")
			return
		}
		if _, err := time.Parse("2006-01-02", activeFrom); err != nil {
			s.redirectTeamsCaptainsError(w, r, "Captain active-from date must use YYYY-MM-DD.")
			return
		}
		var activeTo *string
		if !activeNow && activeToRaw != "" {
			if _, err := time.Parse("2006-01-02", activeToRaw); err != nil {
				s.redirectTeamsCaptainsError(w, r, "Captain active-to date must use YYYY-MM-DD.")
				return
			}
			activeTo = &activeToRaw
		}

		var err error
		if id > 0 {
			_, err = s.DB.Exec(ctx, `
				UPDATE captains
				SET team_id = $1,
				    full_name = $2,
				    email = $3,
				    phone = NULLIF($4, ''),
				    active_from = $5,
				    active_to = $6
				WHERE id = $7
			`, int32(teamID), fullName, emailAddr, phone, activeFrom, activeTo, int32(id))
			if err == nil {
				s.audit(ctx, r, "admin", nil, "captain_updated", "captain", func() *int64 { v := int64(id); return &v }(), map[string]any{"email": emailAddr})
			}
		} else {
			var captainID int32
			err = s.DB.QueryRow(ctx, `
				INSERT INTO captains (team_id, full_name, email, phone, active_from, active_to)
				VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6)
				RETURNING id
			`, int32(teamID), fullName, emailAddr, phone, activeFrom, activeTo).Scan(&captainID)
			if err == nil {
				s.audit(ctx, r, "admin", nil, "captain_created", "captain", func() *int64 { v := int64(captainID); return &v }(), map[string]any{"email": emailAddr})
			}
		}
		if err != nil {
			s.redirectTeamsCaptainsError(w, r, "Could not save captain: "+err.Error())
			return
		}
		s.redirectTeamsCaptainsSuccess(w, r, "Captain saved.")
	}
}

func (s *Server) handleAdminCaptainDeactivate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		captainID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 32)
		if captainID <= 0 {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_, err := s.DB.Exec(ctx, `
			UPDATE captains
			SET active_to = CURRENT_DATE
			WHERE id = $1 AND (active_to IS NULL OR active_to > CURRENT_DATE)
		`, int32(captainID))
		if err != nil {
			s.redirectTeamsCaptainsError(w, r, "Could not deactivate captain: "+err.Error())
			return
		}
		s.audit(ctx, r, "admin", nil, "captain_deactivated", "captain", func() *int64 { v := captainID; return &v }(), map[string]any{})
		s.redirectTeamsCaptainsSuccess(w, r, "Captain deactivated.")
	}
}

func (s *Server) buildAdminTeamsCaptainsPageData(ctx context.Context, r *http.Request) (adminTeamsCaptainsPageData, error) {
	data := adminTeamsCaptainsPageData{
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Filter:     strings.TrimSpace(r.URL.Query().Get("filter")),
		ClubEdit:   adminClubEdit{},
		TeamEdit:   adminTeamEdit{Active: true},
		CaptainEdit: adminCaptainEdit{
			ActiveFrom:  time.Now().Format("2006-01-02"),
			CurrentlyOn: true,
		},
		SuccessMsg: strings.TrimSpace(r.URL.Query().Get("success")),
		ErrorMsg:   strings.TrimSpace(r.URL.Query().Get("error")),
	}

	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM clubs`).Scan(&data.ClubCount); err != nil {
		return data, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM teams`).Scan(&data.TeamCount); err != nil {
		return data, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM teams WHERE active = TRUE`).Scan(&data.ActiveTeams); err != nil {
		return data, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM captains WHERE active_to IS NULL OR active_to >= CURRENT_DATE`).Scan(&data.ActiveCaps); err != nil {
		return data, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM captains WHERE (active_to IS NULL OR active_to >= CURRENT_DATE) AND (TRIM(full_name) = '' OR TRIM(email) = '')`).Scan(&data.MissingCaps); err != nil {
		return data, err
	}

	clubRows, err := s.DB.Query(ctx, `SELECT id, name FROM clubs ORDER BY name`)
	if err != nil {
		return data, err
	}
	defer clubRows.Close()
	for clubRows.Next() {
		var opt adminClubOption
		if err := clubRows.Scan(&opt.ID, &opt.Name); err != nil {
			return data, err
		}
		data.ClubOptions = append(data.ClubOptions, opt)
	}

	teamOptRows, err := s.DB.Query(ctx, `
		SELECT t.id, cl.id, cl.name, t.name
		FROM teams t
		JOIN clubs cl ON cl.id = t.club_id
		ORDER BY cl.name, t.name
	`)
	if err != nil {
		return data, err
	}
	defer teamOptRows.Close()
	for teamOptRows.Next() {
		var id, clubID int32
		var clubName, teamName string
		if err := teamOptRows.Scan(&id, &clubID, &clubName, &teamName); err != nil {
			return data, err
		}
		data.TeamOptions = append(data.TeamOptions, adminTeamOption{
			ID:          id,
			ClubID:      clubID,
			DisplayName: clubName + " - " + teamName,
		})
	}

	filter := "%" + data.Query + "%"
	teamRows, err := s.DB.Query(ctx, `
		SELECT t.id, cl.id, cl.name, t.name, t.active, t.level, COALESCE(t.play_cricket_team_id, ''),
		       cur.id, COALESCE(cur.full_name, ''), COALESCE(cur.email, ''), COALESCE(cur.phone, '')
		FROM teams t
		JOIN clubs cl ON cl.id = t.club_id
		LEFT JOIN LATERAL (
		    SELECT c.id, c.full_name, c.email, COALESCE(c.phone, '') AS phone
		    FROM captains c
		    WHERE c.team_id = t.id AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
		    ORDER BY c.active_from DESC, c.id DESC
		    LIMIT 1
		) cur ON TRUE
		WHERE $1 = '%%'
		   OR cl.name ILIKE $1
		   OR t.name ILIKE $1
		   OR COALESCE(cur.full_name, '') ILIKE $1
		   OR COALESCE(cur.email, '') ILIKE $1
		   OR COALESCE(cur.phone, '') ILIKE $1
		ORDER BY cl.name, t.name
	`, filter)
	if err != nil {
		return data, err
	}
	defer teamRows.Close()
	for teamRows.Next() {
		var row adminTeamCaptainRow
		var captainID *int32
		if err := teamRows.Scan(&row.TeamID, &row.ClubID, &row.ClubName, &row.TeamName, &row.Active, &row.Level, &row.PlayCricketTeamID, &captainID, &row.CaptainName, &row.CaptainEmail, &row.CaptainPhone); err != nil {
			return data, err
		}
		row.CaptainID = captainID
		data.TeamRows = append(data.TeamRows, row)
	}

	captainRows, err := s.DB.Query(ctx, `
		SELECT c.id, cl.name, t.name, c.full_name, c.email, COALESCE(c.phone, ''),
		       TO_CHAR(c.active_from, 'YYYY-MM-DD'),
		       COALESCE(TO_CHAR(c.active_to, 'YYYY-MM-DD'), ''),
		       (c.active_to IS NULL OR c.active_to >= CURRENT_DATE) AS is_active
		FROM captains c
		JOIN teams t ON t.id = c.team_id
		JOIN clubs cl ON cl.id = t.club_id
		WHERE ($1 AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
		             AND (TRIM(c.full_name) = '' OR TRIM(c.email) = ''))
		   OR (NOT $1 AND ($2 = '%%' OR cl.name ILIKE $2 OR t.name ILIKE $2
		                   OR c.full_name ILIKE $2 OR c.email ILIKE $2
		                   OR COALESCE(c.phone, '') ILIKE $2))
		ORDER BY (c.active_to IS NULL OR c.active_to >= CURRENT_DATE) DESC, cl.name, t.name, c.active_from DESC
		LIMIT 300
	`, data.Filter == "missing", filter)
	if err != nil {
		return data, err
	}
	defer captainRows.Close()
	for captainRows.Next() {
		var row adminCaptainRow
		if err := captainRows.Scan(&row.ID, &row.ClubName, &row.TeamName, &row.FullName, &row.Email, &row.Phone, &row.ActiveFrom, &row.ActiveTo, &row.IsActive); err != nil {
			return data, err
		}
		data.CaptainRows = append(data.CaptainRows, row)
	}

	if clubID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("club_id")), 10, 32); clubID > 0 {
		_ = s.DB.QueryRow(ctx, `SELECT id, name, COALESCE(short_name, '') FROM clubs WHERE id = $1`, int32(clubID)).Scan(&data.ClubEdit.ID, &data.ClubEdit.Name, &data.ClubEdit.ShortName)
	}
	if teamID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("team_id")), 10, 32); teamID > 0 {
		var level *int32
		_ = s.DB.QueryRow(ctx, `
			SELECT id, club_id, name, level, active, COALESCE(play_cricket_team_id, '')
			FROM teams WHERE id = $1
		`, int32(teamID)).Scan(&data.TeamEdit.ID, &data.TeamEdit.ClubID, &data.TeamEdit.Name, &level, &data.TeamEdit.Active, &data.TeamEdit.PlayCricketTeamID)
		data.TeamEdit.Level = level
	}
	if captainID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("captain_id")), 10, 32); captainID > 0 {
		var activeFrom, activeTo string
		_ = s.DB.QueryRow(ctx, `
			SELECT id, team_id, full_name, email, COALESCE(phone, ''),
			       TO_CHAR(active_from, 'YYYY-MM-DD'),
			       COALESCE(TO_CHAR(active_to, 'YYYY-MM-DD'), '')
			FROM captains WHERE id = $1
		`, int32(captainID)).Scan(&data.CaptainEdit.ID, &data.CaptainEdit.TeamID, &data.CaptainEdit.FullName, &data.CaptainEdit.Email, &data.CaptainEdit.Phone, &activeFrom, &activeTo)
		data.CaptainEdit.ActiveFrom = activeFrom
		data.CaptainEdit.ActiveTo = activeTo
		data.CaptainEdit.CurrentlyOn = activeTo == ""
	}

	return data, nil
}

func (s *Server) renderAdminTeamsCaptainsPage(w http.ResponseWriter, r *http.Request, data adminTeamsCaptainsPageData) {
	csrfToken := middleware.CSRFToken(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHead(w, "Teams & Captains")
	writeAdminNav(w, csrfToken, r.URL.Path)

	fmt.Fprint(w, `<div class="container-fluid">`)
	fmt.Fprint(w, `<div class="d-flex align-items-center justify-content-between mb-4"><div><h3 class="mb-1">Teams & Captains</h3><p class="text-muted mb-0">Manage clubs, teams, current captain records, and Play-Cricket team IDs in one place.</p></div></div>`)
	if data.SuccessMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(data.SuccessMsg))
	}
	if data.ErrorMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(data.ErrorMsg))
	}
	missingStyle := "bg-light"
	if data.MissingCaps > 0 {
		missingStyle = "bg-warning-subtle"
	}
	fmt.Fprintf(w, `<div class="row row-cols-1 row-cols-md-5 g-3 mb-4">
  <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Clubs</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Teams</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Active teams</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Active captains</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col"><a href="/admin/teams-captains?filter=missing" class="text-decoration-none"><div class="border rounded p-3 %s"><div class="text-muted small">Missing name/email</div><div class="fs-4 fw-semibold">%d</div></div></a></div>
</div>`, data.ClubCount, data.TeamCount, data.ActiveTeams, data.ActiveCaps, missingStyle, data.MissingCaps)

	if data.Filter == "missing" {
		fmt.Fprint(w, `<div class="alert alert-warning d-flex align-items-center justify-content-between">
  <span>Showing active captains with missing name or email address.</span>
  <a href="/admin/teams-captains" class="btn btn-sm btn-outline-secondary">Clear filter</a>
</div>`)
	}

	fmt.Fprintf(w, `<form method="GET" action="/admin/teams-captains" class="row g-2 mb-4">
  <div class="col-md-6"><input type="text" class="form-control" name="q" placeholder="Search club, team, captain, or email" value="%s"></div>
  <div class="col-auto"><button class="btn btn-primary" type="submit">Search</button></div>
  <div class="col-auto"><a class="btn btn-outline-secondary" href="/admin/teams-captains">Clear</a></div>
</form>`, escapeHTML(data.Query))

	fmt.Fprint(w, `<div class="row g-4 mb-4">`)
	fmt.Fprintf(w, `<div class="col-lg-4"><div class="card shadow-sm h-100"><div class="card-header fw-semibold">%s Club</div><div class="card-body">
<form method="POST" action="/admin/clubs/save">
<input type="hidden" name="csrf_token" value="%s">
<input type="hidden" name="club_id" value="%d">
<div class="mb-3"><label class="form-label">Club name</label><input class="form-control" name="name" value="%s" required></div>
<div class="mb-3"><label class="form-label">Short name</label><input class="form-control" name="short_name" value="%s"></div>
<button class="btn btn-primary" type="submit">Save club</button>
<a class="btn btn-outline-secondary ms-2" href="/admin/teams-captains">New</a>
</form></div></div></div>`, map[bool]string{true: "Edit", false: "Add"}[data.ClubEdit.ID > 0], csrfToken, data.ClubEdit.ID, escapeHTML(data.ClubEdit.Name), escapeHTML(data.ClubEdit.ShortName))

	fmt.Fprintf(w, `<div class="col-lg-4"><div class="card shadow-sm h-100"><div class="card-header fw-semibold">%s Team</div><div class="card-body">
<form method="POST" action="/admin/teams/save">
<input type="hidden" name="csrf_token" value="%s">
<input type="hidden" name="team_id" value="%d">
<div class="mb-3"><label class="form-label">Club</label><select class="form-select" name="club_id" required><option value="">Select club...</option>`, map[bool]string{true: "Edit", false: "Add"}[data.TeamEdit.ID > 0], csrfToken, data.TeamEdit.ID)
	for _, opt := range data.ClubOptions {
		selected := ""
		if opt.ID == data.TeamEdit.ClubID {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, opt.ID, selected, escapeHTML(opt.Name))
	}
	levelValue := ""
	if data.TeamEdit.Level != nil {
		levelValue = strconv.Itoa(int(*data.TeamEdit.Level))
	}
	checked := ""
	if data.TeamEdit.Active {
		checked = " checked"
	}
	fmt.Fprintf(w, `</select></div>
<div class="mb-3"><label class="form-label">Team name</label><input class="form-control" name="name" value="%s" required></div>
<div class="mb-3"><label class="form-label">Level</label><input class="form-control" name="level" value="%s" inputmode="numeric"></div>
<div class="mb-3"><label class="form-label">Play-Cricket team ID</label><input class="form-control" name="play_cricket_team_id" value="%s"></div>
<div class="form-check mb-3"><input class="form-check-input" type="checkbox" name="active" id="team-active"%s><label class="form-check-label" for="team-active">Active team</label></div>
<button class="btn btn-primary" type="submit">Save team</button>
<a class="btn btn-outline-secondary ms-2" href="/admin/teams-captains">New</a>
</form></div></div></div>`, escapeHTML(data.TeamEdit.Name), escapeHTML(levelValue), escapeHTML(data.TeamEdit.PlayCricketTeamID), checked)

	fmt.Fprintf(w, `<div class="col-lg-4"><div class="card shadow-sm h-100"><div class="card-header fw-semibold">%s Captain</div><div class="card-body">
<form method="POST" action="/admin/captains/save">
<input type="hidden" name="csrf_token" value="%s">
<input type="hidden" name="captain_id" value="%d">
<div class="mb-3"><label class="form-label">Team</label><select class="form-select" name="team_id" required><option value="">Select team...</option>`, map[bool]string{true: "Edit", false: "Add"}[data.CaptainEdit.ID > 0], csrfToken, data.CaptainEdit.ID)
	for _, opt := range data.TeamOptions {
		selected := ""
		if opt.ID == data.CaptainEdit.TeamID {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, opt.ID, selected, escapeHTML(opt.DisplayName))
	}
	currentChecked := ""
	if data.CaptainEdit.CurrentlyOn {
		currentChecked = " checked"
	}
	fmt.Fprintf(w, `</select></div>
<div class="mb-3"><label class="form-label">Full name</label><input class="form-control" name="full_name" value="%s" required></div>
<div class="mb-3"><label class="form-label">Email</label><input class="form-control" type="email" name="email" value="%s" required></div>
<div class="mb-3"><label class="form-label">Phone</label><input class="form-control" name="phone" value="%s" placeholder="e.g. 07..."></div>
<div class="mb-3"><label class="form-label">Active from</label><input class="form-control" type="date" name="active_from" value="%s" required></div>
<div class="form-check mb-3"><input class="form-check-input" type="checkbox" name="currently_on" id="captain-current"%s><label class="form-check-label" for="captain-current">Currently active captain</label></div>
<div class="mb-3"><label class="form-label">Active to</label><input class="form-control" type="date" name="active_to" value="%s"><div class="form-text">Leave blank for the current captain. Set a date to keep history without deleting the record.</div></div>
<button class="btn btn-primary" type="submit">Save captain</button>
<a class="btn btn-outline-secondary ms-2" href="/admin/teams-captains">New</a>
</form></div></div></div>`, escapeHTML(data.CaptainEdit.FullName), escapeHTML(data.CaptainEdit.Email), escapeHTML(data.CaptainEdit.Phone), escapeHTML(data.CaptainEdit.ActiveFrom), currentChecked, escapeHTML(data.CaptainEdit.ActiveTo))
	fmt.Fprint(w, `</div>`)

	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Teams</div><div class="table-responsive"><table class="table table-hover mb-0"><thead><tr><th>Club</th><th>Team</th><th>Captain</th><th>Play-Cricket</th><th>Status</th><th></th></tr></thead><tbody>`)
	if len(data.TeamRows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No teams found.</td></tr>`)
	}
	for _, row := range data.TeamRows {
		status := `<span class="badge text-bg-success">Active</span>`
		toggleLabel := "Deactivate"
		if !row.Active {
			status = `<span class="badge text-bg-secondary">Inactive</span>`
			toggleLabel = "Activate"
		}
		level := ""
		if row.Level != nil {
			level = fmt.Sprintf(" <span class=\"text-muted small\">L%d</span>", *row.Level)
		}
		captain := `<span class="text-muted">No active captain</span>`
		if row.CaptainName != "" {
			captain = escapeHTML(row.CaptainName)
			if row.CaptainEmail != "" {
				captain += `<div class="text-muted small">` + escapeHTML(row.CaptainEmail) + `</div>`
			}
			if row.CaptainPhone != "" {
				captain += `<div class="text-muted small">` + escapeHTML(row.CaptainPhone) + `</div>`
			}
		}
		pcID := `<span class="text-muted">Not set</span>`
		if row.PlayCricketTeamID != "" {
			pcID = `<code>` + escapeHTML(row.PlayCricketTeamID) + `</code>`
		}
		editCaptain := ""
		if row.CaptainID != nil {
			editCaptain = fmt.Sprintf(`<a class="btn btn-sm btn-outline-secondary" href="/admin/teams-captains?captain_id=%d">Edit captain</a> `, *row.CaptainID)
		}
		fmt.Fprintf(w, `<tr>
<td>%s</td>
<td>%s%s</td>
<td>%s</td>
<td>%s</td>
<td>%s</td>
<td class="text-nowrap">
  <a class="btn btn-sm btn-outline-primary" href="/admin/teams-captains?team_id=%d">Edit team</a>
  %s
  <form method="POST" action="/admin/teams/%d/toggle-active" class="d-inline">
    <input type="hidden" name="csrf_token" value="%s">
    <button class="btn btn-sm btn-outline-warning" type="submit">%s</button>
  </form>
</td>
</tr>`, escapeHTML(row.ClubName), escapeHTML(row.TeamName), level, captain, pcID, status, row.TeamID, editCaptain, row.TeamID, csrfToken, toggleLabel)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)

	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Captain Records</div><div class="table-responsive"><table class="table table-hover mb-0"><thead><tr><th>Captain</th><th>Team</th><th>Dates</th><th>Status</th><th></th></tr></thead><tbody>`)
	if len(data.CaptainRows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="5" class="text-center text-muted py-3">No captains found.</td></tr>`)
	}
	for _, row := range data.CaptainRows {
		status := `<span class="badge text-bg-secondary">Inactive</span>`
		deactivate := ``
		if row.IsActive {
			status = `<span class="badge text-bg-success">Current</span>`
			deactivate = fmt.Sprintf(`<form method="POST" action="/admin/captains/%d/deactivate" class="d-inline">
  <input type="hidden" name="csrf_token" value="%s">
  <button class="btn btn-sm btn-outline-warning" type="submit" onclick="return confirm('Deactivate this captain?')">Deactivate</button>
</form>`, row.ID, csrfToken)
		}
		dateLabel := escapeHTML(row.ActiveFrom) + ` to `
		if row.ActiveTo == "" {
			dateLabel += `present`
		} else {
			dateLabel += escapeHTML(row.ActiveTo)
		}
		fmt.Fprintf(w, `<tr>
<td>%s<div class="text-muted small">%s</div>%s</td>
<td>%s - %s</td>
<td>%s</td>
<td>%s</td>
<td class="text-nowrap">
  <a class="btn btn-sm btn-outline-primary" href="/admin/teams-captains?captain_id=%d">Edit</a>
  %s
</td>
</tr>`, escapeHTML(row.FullName), escapeHTML(row.Email), func() string {
			if row.Phone == "" {
				return ""
			}
			return `<div class="text-muted small">` + escapeHTML(row.Phone) + `</div>`
		}(), escapeHTML(row.ClubName), escapeHTML(row.TeamName), dateLabel, status, row.ID, deactivate)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
	fmt.Fprint(w, `</div>`)
	pageFooter(w)
}

func (s *Server) redirectTeamsCaptainsSuccess(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/admin/teams-captains?success="+urlQueryEscape(message), http.StatusSeeOther)
}

func (s *Server) redirectTeamsCaptainsError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/admin/teams-captains?error="+urlQueryEscape(message), http.StatusSeeOther)
}

func urlQueryEscape(v string) string {
	return url.QueryEscape(v)
}
