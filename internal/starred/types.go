package starred

import "time"

type Entry struct {
	SeasonYear int
	ClubName   string
	ClubKey    string
	ListType   string
	Slot       int
	PlayerName string
	PlayerKey  string
	RawValue   string
	Tags       []string
}

type Amendment struct {
	SeasonYear  int
	ClubName    string
	ClubKey     string
	Sequence    int
	Date        *time.Time
	Outgoing    string
	OutgoingKey string
	Incoming    string
	IncomingKey string
	RawValue    string
	Status      string
	Issue       string
}

type ClubInfo struct {
	ClubName       string
	ClubKey        string
	ListBRule      string
	SubmittedCount int
	NoForm         bool
}

type ClubStatus struct {
	ClubInfo
	CurrentCount  int
	ExpectedCount int
	Compliant     bool
	Reason        string
}

type Snapshot struct {
	SeasonYear int
	Entries    []Entry
	Amendments []Amendment
	Clubs      []ClubInfo
}

type Period struct {
	SeasonYear     int
	ClubName       string
	ClubKey        string
	ListType       string
	PlayerName     string
	PlayerKey      string
	ValidFrom      time.Time
	ValidTo        *time.Time
	Tags           []string
	SourceKind     string
	SourceSequence int
}

type RosterIssue struct {
	ClubName string
	Sequence int
	RawValue string
	Reason   string
}

type Appearance struct {
	MatchID         int64
	SeasonYear      int
	MatchDate       time.Time
	CompetitionType string
	CompetitionName string
	ClubName        string
	ClubKey         string
	TeamName        string
	TeamLevel       int
	PlayingDay      string
	PlayerID        int64
	PlayerName      string
	PlayerKey       string
}

type IdentityMapping struct {
	SeasonYear       int
	ClubKey          string
	StarredPlayerKey string
	PlayerID         int64
	PlayerName       string
}

type Breach struct {
	Appearance           Appearance
	ListType             string
	StarredName          string
	NeedsExemptionReview bool
}

type Candidate struct {
	ClubName       string
	ClubKey        string
	PlayerID       int64
	PlayerName     string
	PlayerKey      string
	FirstXILeague  int
	TopTwoXILeague int
	AllLeague      int
	Percentage     float64
	AlreadyStarred bool
}

type Evaluation struct {
	Breaches   []Breach
	Candidates []Candidate
}

type MappingSuggestion struct {
	ClubName         string
	ClubKey          string
	StarredName      string
	StarredPlayerKey string
	CandidateID      int64
	CandidateName    string
	Distance         int
}

// IdentitySearchResult is a scorecard identity returned by the manual mapping
// search. MatchCount counts distinct scorecards rather than repeated rows.
type IdentitySearchResult struct {
	PlayerID   int64
	PlayerName string
	ClubNames  []string
	MatchCount int
	FirstSeen  time.Time
	LastSeen   time.Time
}
