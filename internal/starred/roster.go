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
			outKey, outOK := resolveActiveKey(active[a.ClubKey]["A"], a.OutgoingKey)
			inKey, inOK := resolveActiveKey(active[a.ClubKey]["B"], a.IncomingKey)
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
		for _, candidateList := range []string{"A", "B"} {
			if key, ok := resolveActiveKey(active[a.ClubKey][candidateList], a.OutgoingKey); ok {
				list, resolvedKey = candidateList, key
				break
			}
		}
		if list == "" && (strings.Contains(rawLower, "b list") || strings.Contains(rawLower, "list b")) {
			list = "B"
		}
		if list == "" && (strings.Contains(rawLower, "a list") || strings.Contains(rawLower, "list a")) {
			list = "A"
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
	return periods, issues
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
