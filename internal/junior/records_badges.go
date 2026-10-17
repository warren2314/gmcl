package junior

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

const BadgeUploadLimit = 2 * 1024 * 1024
const badgeStoredLimit = 8 * 1024 * 1024

var badgeStorageKeyPattern = regexp.MustCompile(`^badges/[0-9]+/[a-f0-9]{64}\.(png|jpg)$`)

func normaliseBadge(data []byte) ([]byte, string, string, int, int, error) {
	if len(data) == 0 || len(data) > BadgeUploadLimit {
		return nil, "", "", 0, 0, fmt.Errorf("%w: upload a PNG or JPEG of at most 2 MB", ErrValidation)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 {
		return nil, "", "", 0, 0, fmt.Errorf("%w: use a genuine PNG or JPEG no larger than 2048 pixels on either side", ErrValidation)
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", "", 0, 0, fmt.Errorf("%w: the image is damaged", ErrValidation)
	}
	var stored bytes.Buffer
	media, extension := "image/png", "png"
	if format == "jpeg" {
		media, extension = "image/jpeg", "jpg"
		err = jpeg.Encode(&stored, decoded, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&stored, decoded)
	}
	if err != nil {
		return nil, "", "", 0, 0, err
	}
	if stored.Len() > badgeStoredLimit {
		return nil, "", "", 0, 0, fmt.Errorf("%w: choose a smaller badge image", ErrValidation)
	}
	return stored.Bytes(), media, extension, config.Width, config.Height, nil
}

func badgeStoragePath(clubID int32, key string) (string, error) {
	if !badgeStorageKeyPattern.MatchString(key) || !strings.HasPrefix(key, fmt.Sprintf("badges/%d/", clubID)) {
		return "", ErrNotFound
	}
	root := strings.TrimSpace(os.Getenv("JUNIOR_UPLOAD_DIR"))
	if root == "" {
		root = "/app/data/junior-uploads"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	file := filepath.Join(root, filepath.FromSlash(key))
	relative, err := filepath.Rel(root, file)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrNotFound
	}
	return file, nil
}

func writeBadgeBlob(file string, data []byte, hash string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(file), ".badge-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// Linking creates the immutable final key atomically, never replacing a
	// different blob or exposing a partially written image to readers.
	if err = os.Link(temporaryName, file); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(file)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrConflict
		}
		handle, err := os.Open(file)
		if err != nil {
			return err
		}
		existing, err := io.ReadAll(io.LimitReader(handle, badgeStoredLimit+1))
		closeErr := handle.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		sum := sha256.Sum256(existing)
		if len(existing) != len(data) || hex.EncodeToString(sum[:]) != hash {
			return fmt.Errorf("%w: existing badge storage failed verification", ErrConflict)
		}
	}
	return nil
}

func (s *Service) SaveBadge(ctx context.Context, p Principal, clubID int32, originalName string, data []byte) (BadgeRevision, error) {
	var result BadgeRevision
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return result, err
	}
	stored, media, extension, width, height, err := normaliseBadge(data)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(stored)
	hash := hex.EncodeToString(sum[:])
	key := fmt.Sprintf("badges/%d/%s.%s", clubID, hash, extension)
	file, err := badgeStoragePath(clubID, key)
	if err != nil {
		return result, err
	}
	if err = writeBadgeBlob(file, stored, hash); err != nil {
		return result, err
	}
	originalName = path.Base(strings.ReplaceAll(originalName, "\\", "/"))
	if len(originalName) > 200 {
		originalName = originalName[:200]
	}
	err = tx.QueryRow(ctx, `INSERT INTO junior_badge_revisions(club_id,storage_key,sha256,media_type,byte_size,width,height,original_name)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(storage_key) DO NOTHING RETURNING id`, clubID, key, hash, media, len(stored), width, height, originalName).Scan(&result.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A repeated identical upload reuses its immutable revision, while the
		// selected-badge event still records this authorised action.
		err = tx.QueryRow(ctx, `SELECT id FROM junior_badge_revisions WHERE club_id=$1 AND storage_key=$2`, clubID, key).Scan(&result.ID)
	}
	if err != nil {
		return result, recordsError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO junior_club_badges(club_id,revision_id) VALUES($1,$2) ON CONFLICT(club_id) DO UPDATE SET revision_id=EXCLUDED.revision_id`, clubID, result.ID); err != nil {
		return result, err
	}
	result.ClubID, result.StorageKey, result.SHA256, result.MediaType, result.ByteSize, result.Width, result.Height, result.OriginalName = clubID, key, hash, media, len(stored), width, height, originalName
	if err = Audit(ctx, tx, p, clubID, "badge", result.ID, "badge_selected", map[string]any{"sha256": hash, "media_type": media, "byte_size": len(stored), "width": width, "height": height}); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Service) ReadBadge(ctx context.Context, p Principal, clubID int32, revisionID int64) (BadgeRevision, []byte, error) {
	var result BadgeRevision
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return result, nil, err
	}
	err := s.DB.QueryRow(ctx, `SELECT badge.id,badge.club_id,badge.storage_key,badge.sha256,badge.media_type,badge.byte_size,badge.width,badge.height,badge.original_name,badge.created_at FROM junior_badge_revisions badge WHERE badge.club_id=$1 AND badge.id=$2`, clubID, revisionID).Scan(&result.ID, &result.ClubID, &result.StorageKey, &result.SHA256, &result.MediaType, &result.ByteSize, &result.Width, &result.Height, &result.OriginalName, &result.CreatedAt)
	if err != nil {
		return result, nil, recordsError(err)
	}
	file, err := badgeStoragePath(clubID, result.StorageKey)
	if err != nil {
		return result, nil, err
	}
	info, err := os.Lstat(file)
	if err != nil {
		return result, nil, err
	}
	if !info.Mode().IsRegular() {
		return result, nil, ErrNotFound
	}
	handle, err := os.Open(file)
	if err != nil {
		return result, nil, err
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, badgeStoredLimit+1))
	if err != nil {
		return result, nil, err
	}
	sum := sha256.Sum256(data)
	if len(data) != result.ByteSize || hex.EncodeToString(sum[:]) != result.SHA256 {
		return result, nil, fmt.Errorf("%w: badge verification failed", ErrConflict)
	}
	return result, data, nil
}
