package junior

import "context"

type ClubWorkspace struct {
	Forms        []FormAssignment
	Competitions []Competition
	Fixtures     []Fixture
	Concessions  []Concession
	Messages     []MessageSummary
}

// ClubWorkspace joins existing authoritative read paths with an explicit club
// scope. Form drafts and unpublished fixtures from other clubs cannot leak in.
func (s *Service) ClubWorkspace(ctx context.Context, p Principal, clubID int32, seasonID int64) (ClubWorkspace, error) {
	var out ClubWorkspace
	if clubID <= 0 {
		return out, ErrNotFound
	}
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return out, err
	}
	p.ClubID = clubID
	formScope := p
	if p.AdminID > 0 {
		formScope.ReadOnly = true
	}
	forms, err := s.FormAssignments(ctx, formScope, 0)
	if err != nil {
		return out, err
	}
	for _, f := range forms {
		if seasonID == 0 || f.SeasonID == seasonID {
			out.Forms = append(out.Forms, f)
		}
	}
	out.Competitions, err = s.ListCompetitions(ctx, p, seasonID)
	if err != nil {
		return out, err
	}
	fixtures, err := s.ListFixtures(ctx, p, 0, true)
	if err != nil {
		return out, err
	}
	for _, f := range fixtures {
		if seasonID == 0 || f.SeasonID == seasonID {
			out.Fixtures = append(out.Fixtures, f)
		}
	}
	out.Concessions, err = s.ListConcessions(ctx, p, ConcessionFilter{SeasonID: seasonID})
	if err != nil {
		return out, err
	}
	out.Messages, err = s.MessagesFiltered(ctx, p, MessageFilter{ClubID: clubID, SeasonID: seasonID})
	return out, err
}
