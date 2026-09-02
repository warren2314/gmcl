package starred

import (
	"sort"
	"strings"
	"time"
)

const LastThreeSecondXIRule = "4.6.3.3.5.1"

// IsWomensAppearance keeps the men's Rule 3.5 review separate from women's cricket.
// Play-Cricket does not expose a single reliable gender flag on every scorecard, so
// the competition, club and team labels are checked together.
func IsWomensAppearance(a Appearance) bool {
	scope := strings.ToLower(strings.Join([]string{a.CompetitionName, a.ClubName, a.TeamName}, " "))
	for _, marker := range []string{"women", "woman", "ladies", "female", "girls"} {
		if strings.Contains(scope, marker) {
			return true
		}
	}
	return false
}

func Evaluate(periods []Period, appearances []Appearance, mappings []IdentityMapping, cutoff time.Time) Evaluation {
	return EvaluateAtCutoffs(periods, appearances, mappings, cutoff, cutoff)
}

// EvaluateAtCutoffs keeps ongoing breach monitoring separate from the fixed
// 31 July candidate review. Breaches use every eligible appearance through
// breachCutoff, while the unstarred-player percentage uses candidateCutoff.
func EvaluateAtCutoffs(periods []Period, appearances []Appearance, mappings []IdentityMapping, breachCutoff, candidateCutoff time.Time) Evaluation {
	mappingBySource := make(map[string]int64)
	for _, m := range mappings {
		mappingBySource[m.ClubKey+"|"+m.StarredPlayerKey] = m.PlayerID
	}

	periodMatches := func(p Period, a Appearance) bool {
		if a.MatchDate.Before(p.ValidFrom) || (p.ValidTo != nil && !a.MatchDate.Before(*p.ValidTo)) {
			return false
		}
		if id := mappingBySource[p.ClubKey+"|"+p.PlayerKey]; id > 0 {
			return a.PlayerID == id
		}
		return p.ClubKey == a.ClubKey && p.PlayerKey == a.PlayerKey
	}
	var out Evaluation
	for _, a := range appearances {
		if a.TeamLevel == 0 || a.MatchDate.After(breachCutoff) || IsWomensAppearance(a) {
			continue
		}
		for _, p := range periods {
			if !periodMatches(p, a) {
				continue
			}
			breach := (p.ListType == "A" && a.TeamLevel > 1) || (p.ListType == "B" && a.TeamLevel > 2)
			if breach && (strings.EqualFold(a.CompetitionType, "League") || strings.EqualFold(a.CompetitionType, "Cup")) {
				out.Breaches = append(out.Breaches, Breach{Appearance: a, ListType: p.ListType, StarredName: p.PlayerName, RuleReference: "3.5", NeedsExemptionReview: hasJuniorTag(p.Tags)})
			}
			break
		}
	}
	out.Breaches = append(out.Breaches, lastThreeSecondXIBreaches(appearances, breachCutoff)...)

	type counts struct {
		sample               Appearance
		first, topTwo, total int
	}
	stats := make(map[string]*counts)
	for _, a := range appearances {
		if a.TeamLevel == 0 || !strings.EqualFold(a.CompetitionType, "League") || a.MatchDate.After(candidateCutoff) || IsWomensAppearance(a) {
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
		if a.TeamLevel == 1 || a.TeamLevel == 2 {
			stats[key].topTwo++
		}
	}
	for _, c := range stats {
		if c.total == 0 || c.topTwo*2 < c.total {
			continue
		}
		starred := false
		previouslyStarred := false
		probe := c.sample
		probe.MatchDate = candidateCutoff
		for _, p := range periods {
			if periodMatches(p, probe) {
				starred = true
				break
			}
			sameIdentity := false
			if id := mappingBySource[p.ClubKey+"|"+p.PlayerKey]; id > 0 {
				sameIdentity = probe.PlayerID > 0 && probe.PlayerID == id
			} else {
				sameIdentity = p.ClubKey == probe.ClubKey && p.PlayerKey == probe.PlayerKey
			}
			if sameIdentity && p.ValidTo != nil && !candidateCutoff.Before(*p.ValidTo) {
				previouslyStarred = true
			}
		}
		if previouslyStarred && !starred {
			continue
		}
		out.Candidates = append(out.Candidates, Candidate{
			ClubName: c.sample.ClubName, ClubKey: c.sample.ClubKey, PlayerID: c.sample.PlayerID,
			PlayerName: c.sample.PlayerName, PlayerKey: c.sample.PlayerKey, FirstXILeague: c.first,
			TopTwoXILeague: c.topTwo, AllLeague: c.total, Percentage: float64(c.topTwo) / float64(c.total), AlreadyStarred: starred,
		})
	}
	sort.Slice(out.Breaches, func(i, j int) bool {
		return out.Breaches[i].Appearance.MatchDate.After(out.Breaches[j].Appearance.MatchDate)
	})
	sort.Slice(out.Candidates, func(i, j int) bool {
		if out.Candidates[i].AlreadyStarred != out.Candidates[j].AlreadyStarred {
			return !out.Candidates[i].AlreadyStarred
		}
		leftClub := strings.ToLower(out.Candidates[i].ClubName)
		rightClub := strings.ToLower(out.Candidates[j].ClubName)
		if leftClub != rightClub {
			return leftClub < rightClub
		}
		leftPlayer := strings.ToLower(out.Candidates[i].PlayerName)
		rightPlayer := strings.ToLower(out.Candidates[j].PlayerName)
		if leftPlayer != rightPlayer {
			return leftPlayer < rightPlayer
		}
		return out.Candidates[i].PlayerID < out.Candidates[j].PlayerID
	})
	return out
}

func appearanceIdentity(a Appearance) string {
	if a.PlayerID > 0 {
		return "id:" + itoa64(a.PlayerID)
	}
	return a.PlayerKey
}

// lastThreeSecondXIBreaches applies rule 4.6.3.3.5.1 to imported scorecards.
// A player is ineligible for a Sunday Second XI appearance in that team's last
// three played league matches when they have six or more First XI league
// appearances but fewer than three Second XI league appearances in the season.
func lastThreeSecondXIBreaches(appearances []Appearance, cutoff time.Time) []Breach {
	type match struct {
		id   int64
		date time.Time
	}
	type counts struct {
		first  map[int64]struct{}
		second map[int64]struct{}
	}

	matchesByClub := make(map[string][]match)
	seenMatches := make(map[string]map[int64]struct{})
	playerCounts := make(map[string]*counts)
	for _, a := range appearances {
		if a.TeamLevel == 0 || a.MatchDate.After(cutoff) || !strings.EqualFold(a.CompetitionType, "League") || IsWomensAppearance(a) {
			continue
		}
		identity := a.ClubKey + "|" + appearanceIdentity(a)
		if playerCounts[identity] == nil {
			playerCounts[identity] = &counts{first: make(map[int64]struct{}), second: make(map[int64]struct{})}
		}
		if a.TeamLevel == 1 {
			playerCounts[identity].first[a.MatchID] = struct{}{}
		}
		if a.TeamLevel == 2 {
			playerCounts[identity].second[a.MatchID] = struct{}{}
		}
		if a.TeamLevel != 2 || !strings.EqualFold(a.PlayingDay, "Sunday") {
			continue
		}
		if seenMatches[a.ClubKey] == nil {
			seenMatches[a.ClubKey] = make(map[int64]struct{})
		}
		if _, seen := seenMatches[a.ClubKey][a.MatchID]; !seen {
			seenMatches[a.ClubKey][a.MatchID] = struct{}{}
			matchesByClub[a.ClubKey] = append(matchesByClub[a.ClubKey], match{id: a.MatchID, date: a.MatchDate})
		}
	}

	lastThree := make(map[string]map[int64]struct{})
	for clubKey, matches := range matchesByClub {
		sort.Slice(matches, func(i, j int) bool {
			if !matches[i].date.Equal(matches[j].date) {
				return matches[i].date.After(matches[j].date)
			}
			return matches[i].id > matches[j].id
		})
		if len(matches) > 3 {
			matches = matches[:3]
		}
		lastThree[clubKey] = make(map[int64]struct{}, len(matches))
		for _, fixture := range matches {
			lastThree[clubKey][fixture.id] = struct{}{}
		}
	}

	breaches := make([]Breach, 0)
	for _, a := range appearances {
		if a.TeamLevel != 2 || a.MatchDate.After(cutoff) || !strings.EqualFold(a.PlayingDay, "Sunday") || !strings.EqualFold(a.CompetitionType, "League") || IsWomensAppearance(a) {
			continue
		}
		if _, inWindow := lastThree[a.ClubKey][a.MatchID]; !inWindow {
			continue
		}
		counts := playerCounts[a.ClubKey+"|"+appearanceIdentity(a)]
		if counts == nil || len(counts.first) < 6 || len(counts.second) >= 3 {
			continue
		}
		breaches = append(breaches, Breach{
			Appearance: a, ListType: "Last 3", RuleReference: LastThreeSecondXIRule,
			FirstXILeague: len(counts.first), SecondXILeague: len(counts.second),
		})
	}
	return breaches
}

func ReviewCutoff(seasonYear int, now time.Time) time.Time {
	cutoff := time.Date(seasonYear, time.July, 31, 23, 59, 59, 0, time.UTC)
	if now.Before(cutoff) {
		return now
	}
	return cutoff
}

func SuggestMappings(periods []Period, appearances []Appearance, mappings []IdentityMapping, asOf time.Time) []MappingSuggestion {
	return SuggestMappingsForRange(periods, appearances, mappings, asOf, asOf)
}

// SuggestMappingsForRange includes list periods that overlap any part of the
// monitored season range, including players removed before the current date.
func SuggestMappingsForRange(periods []Period, appearances []Appearance, mappings []IdentityMapping, from, through time.Time) []MappingSuggestion {
	mapped := make(map[string]bool)
	for _, m := range mappings {
		mapped[m.ClubKey+"|"+m.StarredPlayerKey] = true
	}
	type candidate struct {
		id        int64
		name, key string
	}
	uniqueCandidateIDs := func(values []candidate) []candidate {
		seen := make(map[int64]bool, len(values))
		out := make([]candidate, 0, len(values))
		for _, value := range values {
			if value.id > 0 && !seen[value.id] {
				seen[value.id] = true
				out = append(out, value)
			}
		}
		return out
	}
	byClub := make(map[string][]candidate)
	seenCandidate := make(map[string]bool)
	for _, a := range appearances {
		if a.PlayerID == 0 {
			continue
		}
		key := a.ClubKey + "|" + itoa64(a.PlayerID) + "|" + a.PlayerKey
		if seenCandidate[key] {
			continue
		}
		seenCandidate[key] = true
		byClub[a.ClubKey] = append(byClub[a.ClubKey], candidate{a.PlayerID, a.PlayerName, a.PlayerKey})
	}
	seenSource := make(map[string]bool)
	var out []MappingSuggestion
	for _, p := range periods {
		if p.ValidFrom.After(through) || (p.ValidTo != nil && !p.ValidTo.After(from)) {
			continue
		}
		sourceKey := p.ClubKey + "|" + p.PlayerKey
		if seenSource[sourceKey] || mapped[sourceKey] {
			continue
		}
		seenSource[sourceKey] = true
		best := candidate{}
		bestDistance := 1 << 30
		var exact []candidate
		var aliases []candidate
		for _, c := range byClub[p.ClubKey] {
			if c.key == p.PlayerKey {
				exact = append(exact, c)
				continue
			}
			if likelyNameAlias(p.PlayerName, c.name) {
				aliases = append(aliases, c)
			}
			d := editDistance(p.PlayerKey, c.key)
			if d < bestDistance {
				bestDistance, best = d, c
			}
		}
		exact = uniqueCandidateIDs(exact)
		aliases = uniqueCandidateIDs(aliases)
		if len(exact) == 1 {
			out = append(out, MappingSuggestion{
				ClubName: p.ClubName, ClubKey: p.ClubKey, StarredName: p.PlayerName, StarredPlayerKey: p.PlayerKey,
				CandidateID: exact[0].id, CandidateName: exact[0].name, Distance: 0,
				Confidence: "high", Reason: "Unique exact normalised name at the listed club",
			})
			continue
		}
		if len(exact) > 1 {
			continue
		}
		if len(aliases) == 1 {
			best = aliases[0]
			bestDistance = editDistance(p.PlayerKey, best.key)
		}
		if best.id == 0 {
			continue
		}
		limit := 3
		if len(p.PlayerKey) >= 15 {
			limit = 4
		}
		if len(aliases) == 1 || bestDistance <= limit {
			reason := "Closest unique name within review distance"
			if len(aliases) == 1 {
				reason = "Unique likely name alias at the listed club"
			}
			out = append(out, MappingSuggestion{
				ClubName: p.ClubName, ClubKey: p.ClubKey, StarredName: p.PlayerName, StarredPlayerKey: p.PlayerKey,
				CandidateID: best.id, CandidateName: best.name, Distance: bestDistance,
				Confidence: "review", Reason: reason,
			})
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

// SearchAppearanceIdentities searches every supplied scorecard appearance and
// returns one row per Play-Cricket player ID. All appearances for a matching ID
// contribute to the summary, including rows whose displayed name is a variant.
func SearchAppearanceIdentities(appearances []Appearance, query string, limit int) []IdentitySearchResult {
	query = strings.ToLower(strings.TrimSpace(query))
	if len(query) < 2 {
		return nil
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	type identityStats struct {
		names       map[string]int
		clubs       map[string]struct{}
		matches     map[int64]struct{}
		first, last time.Time
		matched     bool
		rank        int
	}
	stats := make(map[int64]*identityStats)
	for _, appearance := range appearances {
		if appearance.PlayerID <= 0 || appearance.TeamLevel <= 0 || IsWomensAppearance(appearance) {
			continue
		}
		current := stats[appearance.PlayerID]
		if current == nil {
			current = &identityStats{
				names:   make(map[string]int),
				clubs:   make(map[string]struct{}),
				matches: make(map[int64]struct{}),
				rank:    3,
			}
			stats[appearance.PlayerID] = current
		}
		name := strings.TrimSpace(appearance.PlayerName)
		if name != "" {
			current.names[name]++
		}
		if club := strings.TrimSpace(appearance.ClubName); club != "" {
			current.clubs[club] = struct{}{}
		}
		current.matches[appearance.MatchID] = struct{}{}
		if current.first.IsZero() || appearance.MatchDate.Before(current.first) {
			current.first = appearance.MatchDate
		}
		if current.last.IsZero() || appearance.MatchDate.After(current.last) {
			current.last = appearance.MatchDate
		}

		lowerName := strings.ToLower(name)
		playerID := itoa64(appearance.PlayerID)
		rank := 3
		switch {
		case playerID == query || lowerName == query:
			rank = 0
		case strings.HasPrefix(lowerName, query):
			rank = 1
		case strings.Contains(lowerName, query) || strings.Contains(playerID, query):
			rank = 2
		}
		if rank < current.rank {
			current.rank = rank
			current.matched = true
		}
	}

	type rankedResult struct {
		IdentitySearchResult
		rank int
	}
	results := make([]rankedResult, 0)
	for playerID, current := range stats {
		if !current.matched {
			continue
		}
		name, nameCount := "", -1
		for candidate, count := range current.names {
			if count > nameCount || (count == nameCount && candidate < name) {
				name, nameCount = candidate, count
			}
		}
		clubs := make([]string, 0, len(current.clubs))
		for club := range current.clubs {
			clubs = append(clubs, club)
		}
		sort.Strings(clubs)
		results = append(results, rankedResult{IdentitySearchResult: IdentitySearchResult{
			PlayerID: playerID, PlayerName: name, ClubNames: clubs,
			MatchCount: len(current.matches), FirstSeen: current.first, LastSeen: current.last,
		}, rank: current.rank})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].rank != results[j].rank {
			return results[i].rank < results[j].rank
		}
		if results[i].MatchCount != results[j].MatchCount {
			return results[i].MatchCount > results[j].MatchCount
		}
		return results[i].PlayerName < results[j].PlayerName
	})
	if len(results) > limit {
		results = results[:limit]
	}
	out := make([]IdentitySearchResult, len(results))
	for i := range results {
		out[i] = results[i].IdentitySearchResult
	}
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
