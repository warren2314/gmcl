package ineligible

import "testing"

func TestPrepareNativeSubmissionRetainsTrainingClassification(t *testing.T) {
	submission := validNativeSubmission()
	submission.Training = true
	prepared, err := prepareNativeSubmission(submission)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Training {
		t.Fatal("prepared intake lost its training classification")
	}
	if got := prepared.RawData["_training_case"]; got != true {
		t.Fatalf("immutable training marker = %#v, want true", got)
	}

	live := validNativeSubmission()
	livePrepared, err := prepareNativeSubmission(live)
	if err != nil {
		t.Fatal(err)
	}
	if livePrepared.RawSHA256 == prepared.RawSHA256 {
		t.Fatal("training and live submissions must not share an immutable content digest")
	}
}
