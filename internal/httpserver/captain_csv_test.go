package httpserver

import "testing"

func TestParseCaptainCSVLayout_LegacyFormat(t *testing.T) {
	layout, err := parseCaptainCSVLayout([]string{"club", "team", "captain_name", "captain_email"})
	if err != nil {
		t.Fatalf("parseCaptainCSVLayout returned error: %v", err)
	}

	row := layout.buildRow([]string{"Droylsden CC", "Second XI", "Daniel Hugo", "DANNY@example.com"})
	if row.Club != "Droylsden CC" || row.Team != "Second XI" || row.Name != "Daniel Hugo" || row.Email != "danny@example.com" {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestParseCaptainCSVLayout_ContactExportFormat(t *testing.T) {
	layout, err := parseCaptainCSVLayout([]string{"first name", "last name", "email", "MobileTel", "Club", "Team"})
	if err != nil {
		t.Fatalf("parseCaptainCSVLayout returned error: %v", err)
	}

	row := layout.buildRow([]string{"Daniel", "Hugo", "DANNY@example.com", "447700900123", "Droylsden Cricket Club", "2nd XI"})
	if row.Club != "Droylsden Cricket Club" || row.Team != "2nd XI" || row.Name != "Daniel Hugo" || row.Email != "danny@example.com" {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestParseCaptainCSVLayout_RejectsUnexpectedHeader(t *testing.T) {
	if _, err := parseCaptainCSVLayout([]string{"club", "team", "email"}); err == nil {
		t.Fatal("expected error for unexpected header")
	}
}

func TestNormalizeCaptainCSVClubKey(t *testing.T) {
	tests := map[string]string{
		"Droylsden Cricket Club": "droylsden",
		"Droylsden CC":           "droylsden",
		"Edgworth":               "edgworth",
		"Edgworth CC":            "edgworth",
	}

	for input, want := range tests {
		if got := normalizeCaptainCSVClubKey(input); got != want {
			t.Fatalf("normalizeCaptainCSVClubKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeCaptainCSVTeamKey(t *testing.T) {
	tests := map[string]string{
		"First XI":  "1xi",
		"1st XI":    "1xi",
		"Second XI": "2xi",
		"2nd XI":    "2xi",
		"Third XI":  "3xi",
		"3rd XI":    "3xi",
		"Fourth XI": "4xi",
		"4th XI":    "4xi",
	}

	for input, want := range tests {
		if got := normalizeCaptainCSVTeamKey(input); got != want {
			t.Fatalf("normalizeCaptainCSVTeamKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCaptainCSVResolverResolveClubAndTeam(t *testing.T) {
	resolver := &captainCSVResolver{
		clubsByExactKey: map[string]captainCSVClubRef{
			normalizeCaptainCSVExactKey("Droylsden CC"): {ID: 1, Name: "Droylsden CC"},
			normalizeCaptainCSVExactKey("Edgworth CC"):  {ID: 2, Name: "Edgworth CC"},
		},
		clubsByCanonicalKey: map[string][]captainCSVClubRef{
			normalizeCaptainCSVClubKey("Droylsden CC"): {{ID: 1, Name: "Droylsden CC"}},
			normalizeCaptainCSVClubKey("Edgworth CC"):  {{ID: 2, Name: "Edgworth CC"}},
		},
		teamsByClubID: map[int32]captainCSVTeamResolver{
			1: {
				byExactKey: map[string]captainCSVTeamRef{
					normalizeCaptainCSVExactKey("Second XI"): {ID: 10, Name: "Second XI"},
					normalizeCaptainCSVExactKey("Third XI"):  {ID: 11, Name: "Third XI"},
				},
				byCanonicalKey: map[string][]captainCSVTeamRef{
					normalizeCaptainCSVTeamKey("Second XI"): {{ID: 10, Name: "Second XI"}},
					normalizeCaptainCSVTeamKey("Third XI"):  {{ID: 11, Name: "Third XI"}},
				},
			},
			2: {
				byExactKey: map[string]captainCSVTeamRef{
					normalizeCaptainCSVExactKey("Fourth XI"): {ID: 20, Name: "Fourth XI"},
				},
				byCanonicalKey: map[string][]captainCSVTeamRef{
					normalizeCaptainCSVTeamKey("Fourth XI"): {{ID: 20, Name: "Fourth XI"}},
				},
			},
		},
	}

	club, team, clubFound, teamFound := resolver.resolveClubAndTeam("Droylsden Cricket Club", "2nd XI")
	if !clubFound || !teamFound {
		t.Fatalf("expected club/team match, got clubFound=%v teamFound=%v", clubFound, teamFound)
	}
	if club != "Droylsden CC" || team != "Second XI" {
		t.Fatalf("unexpected resolution: club=%q team=%q", club, team)
	}

	club, team, clubFound, teamFound = resolver.resolveClubAndTeam("Edgworth", "4th XI")
	if !clubFound || !teamFound {
		t.Fatalf("expected club/team match, got clubFound=%v teamFound=%v", clubFound, teamFound)
	}
	if club != "Edgworth CC" || team != "Fourth XI" {
		t.Fatalf("unexpected resolution: club=%q team=%q", club, team)
	}
}
