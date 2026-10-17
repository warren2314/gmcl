package junior

import "time"

type Season struct {
	ID                 int64
	Name               string
	StartDate, EndDate time.Time
	State              string
	LinkedSeasonID     *int32
}

type SeasonInput struct {
	ID                 int64
	Name               string
	StartDate, EndDate time.Time
	State              string
	LinkedSeasonID     *int32
}

type ClubInput struct {
	ID                      int32
	Name, ShortName, Source string
}
type ClubSummary struct {
	ID                      int32
	Name, ShortName, State  string
	TeamCount, ContactCount int64
}
type Team struct {
	ID       int64
	ClubID   int32
	Name     string
	Archived bool
}
type TeamInput struct {
	ID       int64
	ClubID   int32
	Name     string
	Archived bool
}

type ContactRole struct {
	ID, ContactID, SeasonID int64
	Role, AgeGroup          string
	TeamID                  *int64
	TeamName                string
	StartsOn                time.Time
	EndsOn                  *time.Time
}

type Contact struct {
	ID                     int64
	ClubID                 int32
	FullName, Email, Phone string
	Archived               bool
	Roles                  []ContactRole
}

// A contact may have several dated role assignments. Supplying Role adds one
// assignment; changing adult details never grants a portal account.
type ContactInput struct {
	ID                     int64
	ClubID                 int32
	SeasonID               int64
	FullName, Email, Phone string
	Archived               bool
	Role, AgeGroup         string
	TeamID                 *int64
	StartsOn               time.Time
	EndsOn                 *time.Time
}

type Participation struct {
	SeasonID          int64
	SeasonName, State string
	Provenance        []byte
}
type ClubProfile struct {
	Club          ClubSummary
	Season        Season
	Teams         []Team
	Contacts      []Contact
	Participation []Participation
	Badges        []BadgeRevision
	BadgeID       int64
}

type BadgeRevision struct {
	ID                                          int64
	ClubID                                      int32
	StorageKey, SHA256, MediaType, OriginalName string
	ByteSize, Width, Height                     int
	CreatedAt                                   time.Time
}

type RosterRow struct {
	Number                                                        int
	ClubName, ContactName, Email, Phone, Role, AgeGroup, TeamName string
	Problem                                                       string
	SuggestedClubID                                               int32
	CandidateClubIDs                                              []int32
}

type RosterMapping struct {
	RowNumber                                              int
	ClubID                                                 int32
	ContactID, TeamID                                      int64
	CreateContact, CreateTeam, ActivateParticipation, Skip bool
	Reason                                                 string
}

type RosterApplyResult struct{ RowsApplied, RowsSkipped, ClubsActivated, ContactsCreated, TeamsCreated, RolesAdded int }
type RosterPreview struct {
	ID, SeasonID              int64
	SourceName, SHA256, State string
	Rows                      []RosterRow
	Clubs                     []ClubSummary
	Contacts                  []Contact
	Teams                     []Team
	Result                    RosterApplyResult
	Review                    []RosterMapping
}

func RoleLabel(role string) string {
	switch role {
	case "junior_lead":
		return "Junior Lead"
	case "safeguarding_officer":
		return "Safeguarding Officer"
	case "coach":
		return "Coach / age-group contact"
	}
	return role
}

func ParticipationLabel(state string) string {
	switch state {
	case "active":
		return "Active"
	case "withdrawn":
		return "Withdrawn this season"
	}
	return "Not entered this season"
}
