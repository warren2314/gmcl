package starred

import (
	"sort"
	"strings"
	"time"
)

func Evaluate(periods []Period, appearances []Appearance, mappings []IdentityMapping, cutoff time.Time) Evaluation {
	mappingBySource := make(map[string]int64)
	for _, m := range mappings {
		mappingBySource[m.ClubKey+"|"+m.StarredPlayerKey] = m.PlayerID
	}

	periodMatches := func(p Period, a Appearance) bool {
		if p.ClubKey != a.ClubKey || a.MatchDate.Before(p.ValidFrom) || (p.ValidTo != nil && !a.MatchDate.Before(*p.ValidTo)) {
			return false
		}
		if id := mappingBySource[p.ClubKey+"|"+p.PlayerKey]; id > 0 {
			return a.PlayerID == id
		}
		return p.PlayerKey == a.PlayerKey
	}
	var out Evaluation
	for _, a := range appearances {
		if a.TeamLevel == 0 || a.MatchDate.After(cutoff) {
			continue
		}
		for _, p := range periods {
			if !periodMatches(p, a) {
				continue
			}
			breach := (p.ListType == "A" && a.TeamLevel > 1) || (p.ListType == "B" && a.TeamLevel > 2)
			if breach && (strings.EqualFold(a.CompetitionType, "League") || strings.EqualFold(a.CompetitionType, "Cup")) {
				out.Breaches = append(out.Breaches, Breach{Appearance: a, ListType: p.ListType, StarredName: p.PlayerName, NeedsExemptionReview: hasJuniorTag(p.Tags)})
			}
			break
		}
	}

	type counts struct {
		sample       Appearance
		first, total int
	}
	stats := make(map[string]*counts)
	for _, a := range appearances {
		if !strings.EqualFold(a.CompetitionType, "League") || a.MatchDate.After(cutoff) {
			continue
		}
		identity := a.PlayerKey
		if a.PlayerID > 0 {
			identity = "id:" + itoa64(a.PlayerID)
		}
		key := a.ClubKey + "|" + identity
		if stats[key] == nil {
			stats[key] = &counts{sample: a}
		}
		stats[key].total++
		if a.TeamLevel == 1 {
			stats[key].first++
		}
	}
	for _, c := range stats {
		if c.total == 0 || c.first*2 < c.total {
			continue
		}
		starred := false
		probe := c.sample
		probe.MatchDate = cutoff
		for _, p := range periods {
			if periodMatches(p, probe) {
				starred = true
				break
			}
		}
		out.Candidates = append(out.Candidates, Candidate{
			ClubName: c.sample.ClubName, ClubKey: c.sample.ClubKey, PlayerID: c.sample.PlayerID,
			PlayerName: c.sample.PlayerName, PlayerKey: c.sample.PlayerKey, FirstXILeague: c.first,
			AllLeague: c.total, Percentage: float64(c.first) / float64(c.total), AlreadyStarred: starred,
		})
	}
	sort.Slice(out.Breaches, func(i, j int) bool {
		return out.Breaches[i].Appearance.MatchDate.After(out.Breaches[j].Appearance.MatchDate)
	})
	sort.Slice(out.Candidates, func(i, j int) bool {
		if out.Candidates[i].AlreadyStarred != out.Candidates[j].AlreadyStarred {
			return !out.Candidates[i].AlreadyStarred
		}
		if out.Candidates[i].Percentage != out.Candidates[j].Percentage {
			return out.Candidates[i].Percentage > out.Candidates[j].Percentage
		}
		return out.Candidates[i].PlayerName < out.Candidates[j].PlayerName
	})
	return out
}

func ReviewCutoff(seasonYear int, now time.Time) time.Time {
	cutoff := time.Date(seasonYear, time.June, 30, 23, 59, 59, 0, time.UTC)
	if now.Before(cutoff) {
		return now
	}
	return cutoff
}

func SuggestMappings(periods []Period, appearances []Appearance, mappings []IdentityMapping, asOf time.Time) []MappingSuggestion {
	mapped := make(map[string]bool)
	for _, m := range mappings {
		mapped[m.ClubKey+"|"+m.StarredPlayerKey] = true
	}
	type candidate struct {
		id        int64
		name, key string
	}
	byClub := make(map[string][]candidate)
	seenCandidate := make(map[string]bool)
	for _, a := range appearances {
		if a.PlayerID == 0 {
			continue
		}
		key := a.ClubKey + "|" + itoa64(a.PlayerID)
		if seenCandidate[key] {
			continue
		}
		seenCandidate[key] = true
		byClub[a.ClubKey] = append(byClub[a.ClubKey], candidate{a.PlayerID, a.PlayerName, a.PlayerKey})
	}
	seenSource := make(map[string]bool)
	var out []MappingSuggestion
	for _, p := range periods {
		if asOf.Before(p.ValidFrom) || (p.ValidTo != nil && !asOf.Before(*p.ValidTo)) {
			continue
		}
		sourceKey := p.ClubKey + "|" + p.PlayerKey
		if seenSource[sourceKey] || mapped[sourceKey] {
			continue
		}
		seenSource[sourceKey] = true
		best := candidate{}
		bestDistance := 1 << 30
		for _, c := range byClub[p.ClubKey] {
			if c.key == p.PlayerKey {
				bestDistance = 0
				best = c
				break
			}
			d := editDistance(p.PlayerKey, c.key)
			if d < bestDistance {
				bestDistance, best = d, c
			}
		}
		if best.id == 0 || bestDistance == 0 {
			continue
		}
		limit := 3
		if len(p.PlayerKey) >= 15 {
			limit = 4
		}
		if bestDistance <= limit {
			out = append(out, MappingSuggestion{p.ClubName, p.ClubKey, p.PlayerName, p.PlayerKey, best.id, best.name, bestDistance})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		if out[i].ClubName != out[j].ClubName {
			return out[i].ClubName < out[j].ClubName
		}
		return out[i].StarredName < out[j].StarredName
	})
	return out
}

func hasJuniorTag(tags []string) bool {
	for _, tag := range tags {
		if strings.Contains(tag, "17") || strings.Contains(tag, "18") {
			return true
		}
	}
	return false
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
