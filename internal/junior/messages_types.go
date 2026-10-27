package junior

import "time"

type Recipient struct {
	ClubID      int32
	ContactID   int64
	Email, Name string
}

// MessageIntent is constructed by trusted workflow services, not decoded from
// public JSON. Key is a deterministic business idempotency key.
type MessageIntent struct {
	SeasonID           int64
	ClubID             int32
	Kind, RecordType   string
	RecordID           int64
	Key, Subject, Body string
	Recipients         []Recipient
}
type MessageSummary struct {
	ID, SeasonID, RecordID                     int64
	ClubID                                     int32
	Kind, RecordType, Subject, Body, Status    string
	CreatedAt                                  time.Time
	RecipientCount, AcceptedCount, FailedCount int64
	Recipients                                 []Recipient
}
type AudienceFilter struct {
	SeasonID, CompetitionID int64
	ClubID                  int32
	Role, AgeGroup          string
	ClubIDs                 []int32
	ContactIDs              []int64
	// Contacts mode must never fall back to sending to the entire group.
	Selection string
}
type MessageFilter struct {
	ClubID       int32
	SeasonID     int64
	ProblemsOnly bool
}
type AudiencePreview struct {
	Recipients []Recipient
	Hash       string
}
type IncomingMessage struct {
	ID                         int64
	ClubID                     int32
	From, Subject, Body, State string
	CreatedAt                  time.Time
}
