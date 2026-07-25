package starred

import (
	"sort"
	"strings"
	"time"
)

type activePeriod struct {
	index  int
	period Period
}

func BuildPeriods(s Snapshot, seasonStart time.Time) ([]Period, []RosterIssue) {
	var periods []Period
	active := make(map[string]map[string]map[string]activePeriod)
	ensure := func(club, list string) map[string]activePeriod {
		if active[club] == nil {
			active[club] = make(map[string]map[string]activePeriod)
		}
		if active[club][list] == nil {
			active[club][list] = make(map[string]activePeriod)
		}
		return active[club][list]
	}
	add := func(clubName, club, list, name, key string, from time.Time, tags []string, kind string, seq int) {
		p := Period{SeasonYear: s.SeasonYear, ClubName: clubName, ClubKey: club, ListType: list, PlayerName: name, PlayerKey: key, ValidFrom: from, Tags: tags, SourceKind: kind, SourceSequence: seq}
		periods = append(periods, p)
		ensure(club, list)[key] = activePeriod{index: len(periods) - 1, period: p}
	}
	closePeriod := func(club, list, key string, at time.Time) {
		ap := active[club][list][key]
		periods[ap.index].ValidTo = &at
		delete(active[club][list], key)
	}
	for _, e := range s.Entries {
		add(e.ClubName, e.ClubKey, e.ListType, e.PlayerName, e.PlayerKey, seasonStart, e.Tags, "base", 0)
	}

	ams := append([]Amendment(nil), s.Amendments...)
	sort.SliceStable(ams, func(i, j int) bool {
		if ams[i].ClubKey != ams[j].ClubKey {
			return ams[i].ClubKey < ams[j].ClubKey
		}
		if ams[i].Date != nil && ams[j].Date != nil && !ams[i].Date.Equal(*ams[j].Date) {
			return ams[i].Date.Before(*ams[j].Date)
		}
		return ams[i].Sequence < ams[j].Sequence
	})
	var issues []RosterIssue
	for _, a := range ams {
		if a.Date == nil || a.IncomingKey == "" {
			issues = append(issues, RosterIssue{a.ClubName, a.Sequence, a.RawValue, a.Issue})
			continue
		}
		rawLower := strings.ToLower(a.RawValue)
		if strings.Contains(rawLower, "to go list b") && strings.Contains(rawLower, "to go to list a") {
			outKey, outOK := resolveActivePlayer(active[a.ClubKey]["A"], a.OutgoingKey, a.Outgoing)
			inKey, inOK := resolveActivePlayer(active[a.ClubKey]["B"], a.IncomingKey, a.Incoming)
			if !outOK || !inOK {
				issues = append(issues, RosterIssue{a.ClubName, a.Sequence, a.RawValue, "could not resolve List A/List B swap"})
				continue
			}
			out := active[a.ClubKey]["A"][outKey].period
			in := active[a.ClubKey]["B"][inKey].period
			closePeriod(a.ClubKey, "A", outKey, *a.Date)
			closePeriod(a.ClubKey, "B", inKey, *a.Date)
			add(a.ClubName, a.ClubKey, "B", out.PlayerName, out.PlayerKey, *a.Date, out.Tags, "amendment", a.Sequence)
			add(a.ClubName, a.ClubKey, "A", in.PlayerName, in.PlayerKey, *a.Date, in.Tags, "amendment", a.Sequence)
			continue
		}

		list, resolvedKey := "", ""
		hintedList := ""
		if strings.Contains(rawLower, "b list") || strings.Contains(rawLower, "list b") {
			hintedList = "B"
		} else if strings.Contains(rawLower, "a list") || strings.Contains(rawLower, "list a") {
			hintedList = "A"
		}
		type amendmentTarget struct {
			list string
			key  string
			from time.Time
			kind string
		}
		var targets []amendmentTarget
		for _, candidateList := range []string{"A", "B"} {
			if key, ok := resolveActivePlayer(active[a.ClubKey][candidateList], a.OutgoingKey, a.Outgoing); ok {
				period := active[a.ClubKey][candidateList][key].period
				targets = append(targets, amendmentTarget{list: candidateList, key: key, from: period.ValidFrom, kind: period.SourceKind})
			}
		}
		if len(targets) > 0 {
			if hintedList != "" {
				for _, target := range targets {
					if target.list == hintedList {
						list, resolvedKey = target.list, target.key
						break
					}
				}
			}
			// A published player can legitimately be on both lists after another
			// amendment on the same date. Prefer their established list place over
			// a just-created amendment period instead of silently choosing List A.
			sort.SliceStable(targets, func(i, j int) bool {
				if !targets[i].from.Equal(targets[j].from) {
					return targets[i].from.Before(targets[j].from)
				}
				if targets[i].kind != targets[j].kind {
					return targets[i].kind == "base"
				}
				return targets[i].list < targets[j].list
			})
			if list == "" {
				list, resolvedKey = targets[0].list, targets[0].key
			}
		}
		if list == "" {
			list = hintedList
		}
		// The published sheet is maintained as a current list while also retaining
		// its amendment log. When the outgoing player is no longer present and the
		// incoming player is already active, the list has already incorporated the
		// amendment. Treat replaying it as an idempotent success.
		if resolvedKey == "" && activePlayerList(active[a.ClubKey], a.IncomingKey, a.Incoming) != "" {
			continue
		}
		if resolvedKey != "" {
			closePeriod(a.ClubKey, list, resolvedKey, *a.Date)
		}
		if list == "" {
			issues = append(issues, RosterIssue{a.ClubName, a.Sequence, a.RawValue, "outgoing player was not found on an active list"})
			continue
		}
		name, tags := parsePlayerCell(a.Incoming)
		add(a.ClubName, a.ClubKey, list, name, NormalizeName(name), *a.Date, tags, "amendment", a.Sequence)
	}
	return dedupePeriods(periods), issues
}

// dedupePeriods protects the review from duplicate rows in the published
// spreadsheet. A player can only occupy a given list once for the same
// validity window, even if the source accidentally puts them in two slots.
func dedupePeriods(periods []Period) []Period {
	type identity struct {
		season             int
		club, list, player string
		validFrom, validTo time.Time
		hasValidTo         bool
	}
	out := make([]Period, 0, len(periods))
	indexes := make(map[identity]int, len(periods))
	for _, period := range periods {
		key := identity{
			season: period.SeasonYear, club: period.ClubKey, list: period.ListType,
			player: period.PlayerKey, validFrom: period.ValidFrom,
		}
		if period.ValidTo != nil {
			key.validTo, key.hasValidTo = *period.ValidTo, true
		}
		if index, duplicate := indexes[key]; duplicate {
			seenTag := make(map[string]bool, len(out[index].Tags))
			for _, tag := range out[index].Tags {
				seenTag[tag] = true
			}
			for _, tag := range period.Tags {
				if !seenTag[tag] {
					out[index].Tags = append(out[index].Tags, tag)
					seenTag[tag] = true
				}
			}
			continue
		}
		indexes[key] = len(out)
		out = append(out, period)
	}
	return out
}

func resolveActiveKey(values map[string]activePeriod, target string) (string, bool) {
	if _, ok := values[target]; ok {
		return target, true
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return closestUnique(target, keys)
}

func resolveActivePlayer(values map[string]activePeriod, targetKey, targetName string) (string, bool) {
	if key, ok := resolveActiveKey(values, targetKey); ok {
		return key, true
	}
	var matched string
	for key, value := range values {
		if !likelyRosterNameAlias(targetName, value.period.PlayerName) {
			continue
		}
		if matched != "" {
			return "", false
		}
		matched = key
	}
	return matched, matched != ""
}

func activePlayerList(lists map[string]map[string]activePeriod, playerKey, playerName string) string {
	for _, list := range []string{"A", "B"} {
		if _, ok := resolveActivePlayer(lists[list], playerKey, playerName); ok {
			return list
		}
	}
	return ""
}

func likelyRosterNameAlias(source, candidate string) bool {
	sourceWords := strings.Fields(strings.ToLower(source))
	candidateWords := strings.Fields(strings.ToLower(candidate))
	if len(sourceWords) < 2 || len(candidateWords) < 2 {
		return false
	}
	sourceFirst, candidateFirst := NormalizeName(sourceWords[0]), NormalizeName(candidateWords[0])
	sourceLast, candidateLast := NormalizeName(sourceWords[len(sourceWords)-1]), NormalizeName(candidateWords[len(candidateWords)-1])
	if sourceFirst == "" || candidateFirst == "" || sourceLast == "" || candidateLast == "" {
		return false
	}
	lastMatches := sourceLast == candidateLast ||
		(len(sourceLast) >= 4 && len(candidateLast) >= 4 && editDistance(sourceLast, candidateLast) <= 2)
	if !lastMatches {
		return false
	}
	if sourceFirst == candidateFirst ||
		(len(sourceFirst) >= 4 && len(candidateFirst) >= 4 && editDistance(sourceFirst, candidateFirst) <= 2) ||
		(len(sourceFirst) >= 3 && len(candidateFirst) >= 3 &&
			(strings.HasPrefix(sourceFirst, candidateFirst[:3]) || strings.HasPrefix(candidateFirst, sourceFirst[:3]))) {
		return true
	}
	firstAliases := map[string]string{
		"mike": "michael", "michael": "michael",
	}
	return firstAliases[sourceFirst] != "" && firstAliases[sourceFirst] == firstAliases[candidateFirst]
}
