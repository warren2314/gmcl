package httpserver

import (
	"fmt"
	"net/mail"
	"strings"
	"time"

	"cricket-ground-feedback/internal/starred"
)

const starredClubEmailDomain = "gtrmcrcricket.co.uk"

const starredEmailActionFooter = `Please update your starred list here:
https://docs.google.com/forms/d/e/1FAIpQLSeR6_FyGDrAY1PwFLGCMXbuRpo7Gx2jj3l_HxsNOSuoFJ-J4Q/viewform

Or let us know if you believe this review should be reconsidered.

Full Starred Rules are available here:
https://www.gtrmcrcricket.co.uk/pages/rules-3-5`

func starredClubEmail(clubKey, clubName string) (string, error) {
	localPart := starred.NormalizeName(clubKey)
	if localPart == "" {
		localPart = starred.NormalizeClub(clubName)
	}
	if localPart == "" {
		return "", fmt.Errorf("club email cannot be derived without a club")
	}
	address := localPart + "@" + starredClubEmailDomain
	if _, err := mail.ParseAddress(address); err != nil {
		return "", fmt.Errorf("invalid club email %q: %w", address, err)
	}
	return address, nil
}

func starredCandidateRequestEmail(row starredPlayerReviewRow, cutoff time.Time) (string, string) {
	subject := fmt.Sprintf("GMCL List B review request - %s", row.PlayerName)
	body := fmt.Sprintf(`Hello %s,

The GMCL starred-player review has identified the following player for a List B review:

Club: %s
Player: %s
Review date: %s
1st XI appearances: %d
1st XI team fixtures: %d
1st XI percentage: %.1f%%

The player appeared in at least 50%% of the club's 1st XI fixtures through the review date and may need adding to List B.

Please review the player's status and respond to the league if any correction or relevant information is required.

%s

Regards,
Greater Manchester Cricket League`, strings.TrimSpace(row.ClubName), row.ClubName, row.PlayerName, cutoff.Format("02 January 2006"), row.Counts[1], row.TeamGames[1], row.FirstPct, starredEmailActionFooter)
	return subject, body
}
