package junior

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
	"time"
)

func TestRosterCSVValidatesExplicitSourceRows(t *testing.T) {
	data := []byte("club_name,contact_name,email,phone,role,age_group,team_name\nExample CC,Alex Adult,ALEX@example.invalid,,coach,U11,U11 A\nMissing CC,,,,,,\nExample CC,Wrong Role,other@example.invalid,,captain,,\nExample CC,Lead,lead@example.invalid,,junior_lead,,U11 A\n")
	rows, err := ParseRosterCSV(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0].Email != "alex@example.invalid" || rows[0].Problem != "" || rows[1].Problem != "" || rows[2].Problem == "" || rows[3].Problem == "" {
		t.Fatalf("unexpected review rows: %+v", rows)
	}
	for _, data := range [][]byte{nil, []byte("club_name,club_name\nA,A\n"), []byte("unknown\nA\n"), []byte("club_name,email\nA\n"), bytes.Repeat([]byte("x"), RosterUploadLimit+1)} {
		if _, err := ParseRosterCSV(data); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid source accepted: %v", err)
		}
	}
	large := []byte("club_name\n" + string(bytes.Repeat([]byte("A\n"), RosterRowLimit+1)))
	if _, err := ParseRosterCSV(large); !errors.Is(err, ErrValidation) {
		t.Fatalf("row bound: %v", err)
	}
}

func TestBadgeNormalisationRejectsNonImagesAndStripsTrailingContent(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			picture := image.NewRGBA(image.Rect(0, 0, 2, 3))
			picture.Set(0, 0, color.RGBA{R: 255, A: 255})
			var source bytes.Buffer
			var err error
			if format == "png" {
				err = png.Encode(&source, picture)
			} else {
				err = jpeg.Encode(&source, picture, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			data := append(source.Bytes(), []byte("<script>trailing upload content</script>")...)
			stored, media, extension, width, height, err := normaliseBadge(data)
			if err != nil {
				t.Fatal(err)
			}
			if width != 2 || height != 3 || media == "" || extension == "" || bytes.Contains(stored, []byte("script")) {
				t.Fatalf("badge not normalised: %s %s %dx%d", media, extension, width, height)
			}
			if _, _, err = image.Decode(bytes.NewReader(stored)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, data := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), []byte("not an image"), bytes.Repeat([]byte("x"), BadgeUploadLimit+1)} {
		if _, _, _, _, _, err := normaliseBadge(data); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid badge accepted: %v", err)
		}
	}
	var oversized bytes.Buffer
	if err := png.Encode(&oversized, image.NewGray(image.Rect(0, 0, 2049, 1))); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := normaliseBadge(oversized.Bytes()); !errors.Is(err, ErrValidation) {
		t.Fatalf("dimension bound: %v", err)
	}
}

func TestRecordContactRolesRequireAdultContactAndDatedAgeScope(t *testing.T) {
	today := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for _, role := range []string{"junior_lead", "safeguarding_officer", "coach"} {
		in := ContactInput{FullName: "Adult Contact", Email: "adult@example.invalid", Role: role, StartsOn: today}
		if role == "coach" {
			in.AgeGroup = "U9"
		}
		if err := validateRecordContact(&in); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []ContactInput{{FullName: "Adult Contact", Role: "coach", AgeGroup: "U9", StartsOn: today}, {FullName: "Adult Contact", Phone: "0123456789", Role: "junior_lead", StartsOn: today}, {FullName: "Adult Contact", Email: "a@example.invalid", Role: "coach", AgeGroup: "U10", StartsOn: today}, {FullName: "Adult Contact", Email: "a@example.invalid", Role: "safeguarding_officer", AgeGroup: "U11", StartsOn: today}} {
		if err := validateRecordContact(&in); !errors.Is(err, ErrValidation) {
			t.Fatalf("invalid contact accepted: %+v: %v", in, err)
		}
	}
}
