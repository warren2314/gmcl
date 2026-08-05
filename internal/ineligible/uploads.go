package ineligible

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var driveFileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{10,200}$`)

// UploadDownloader fetches only a Drive file ID parsed from the source row.
type UploadDownloader interface {
	DownloadUpload(context.Context, string, int64) (UploadDownload, error)
}

// UploadDownload owns an open, bounded Drive media response. The caller must
// close Body whether validation/storage succeeds or fails.
type UploadDownload struct {
	DriveFileID string
	Name        string
	ContentType string
	Size        int64
	Body        io.ReadCloser
}

// StoredAttachment is the immutable content-addressed artifact linked to an
// exact intake revision by PGStore.
type StoredAttachment struct {
	DriveFileID  string
	SourceURL    string
	OriginalName string
	ContentType  string
	SizeBytes    int64
	SHA256       string
	StorageKey   string
}

type uploadReference struct {
	FileID    string
	SourceURL string
}

func parseUploadReferences(cell string) ([]uploadReference, error) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(cell, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	result := make([]uploadReference, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		reference := strings.TrimSpace(part)
		if reference == "" {
			continue
		}
		fileID, err := driveFileIDFromReference(reference)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[fileID]; exists {
			continue
		}
		seen[fileID] = struct{}{}
		result = append(result, uploadReference{FileID: fileID, SourceURL: reference})
	}
	return result, nil
}

func driveFileIDFromReference(reference string) (string, error) {
	if driveFileIDPattern.MatchString(reference) {
		return reference, nil
	}
	u, err := url.Parse(reference)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("File Upload contains an invalid Google Drive reference")
	}
	host := strings.ToLower(u.Hostname())
	if host != "drive.google.com" && host != "docs.google.com" {
		return "", fmt.Errorf("File Upload URL host %q is not permitted", host)
	}
	fileID := strings.TrimSpace(u.Query().Get("id"))
	if fileID == "" {
		segments := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
		for index := 0; index+1 < len(segments); index++ {
			if segments[index] == "d" {
				decoded, decodeErr := url.PathUnescape(segments[index+1])
				if decodeErr == nil {
					fileID = decoded
				}
				break
			}
		}
	}
	if !driveFileIDPattern.MatchString(fileID) {
		return "", fmt.Errorf("File Upload URL does not contain a valid Google Drive file ID")
	}
	return fileID, nil
}

func storeUpload(ctx context.Context, cfg Config, reference uploadReference, download UploadDownload, maxBytes int64) (StoredAttachment, error) {
	if download.Body == nil {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s returned no media body", reference.FileID)
	}
	defer download.Body.Close()
	if download.DriveFileID != reference.FileID {
		return StoredAttachment{}, fmt.Errorf("Google Drive response ID did not match the requested file")
	}
	contentType, err := normalizeContentType(download.ContentType)
	if err != nil {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s: %w", reference.FileID, err)
	}
	if !contentTypeAllowed(cfg.UploadContentTypes, contentType) {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s content type %q is not permitted", reference.FileID, contentType)
	}
	if download.Size < 1 {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s has no valid size", reference.FileID)
	}
	if download.Size > maxBytes {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s exceeds the configured byte limit", reference.FileID)
	}
	originalName := truncateUTF8(strings.ReplaceAll(strings.TrimSpace(download.Name), "\x00", ""), 512)
	if originalName == "" {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s has no valid original filename", reference.FileID)
	}

	temporaryDir := filepath.Join(cfg.UploadDir, ".tmp")
	if err := os.MkdirAll(temporaryDir, 0700); err != nil {
		return StoredAttachment{}, fmt.Errorf("create ineligible upload temporary directory: %w", err)
	}
	temporaryFile, err := os.CreateTemp(temporaryDir, "drive-upload-*")
	if err != nil {
		return StoredAttachment{}, fmt.Errorf("create ineligible upload temporary file: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	keepTemporary := false
	defer func() {
		_ = temporaryFile.Close()
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporaryFile.Chmod(0600); err != nil {
		return StoredAttachment{}, fmt.Errorf("protect ineligible upload temporary file: %w", err)
	}
	hasher := sha256.New()
	written, err := copyUploadWithContext(ctx, io.MultiWriter(temporaryFile, hasher), io.LimitReader(download.Body, maxBytes+1))
	if err != nil {
		return StoredAttachment{}, fmt.Errorf("retain Google Drive file %s: %w", reference.FileID, err)
	}
	if written > maxBytes {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s exceeds the configured byte limit", reference.FileID)
	}
	if written != download.Size {
		return StoredAttachment{}, fmt.Errorf("Google Drive file %s size changed during download", reference.FileID)
	}
	if err := temporaryFile.Sync(); err != nil {
		return StoredAttachment{}, fmt.Errorf("sync retained Google Drive file %s: %w", reference.FileID, err)
	}
	if err := temporaryFile.Close(); err != nil {
		return StoredAttachment{}, fmt.Errorf("close retained Google Drive file %s: %w", reference.FileID, err)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	storageKey := filepath.ToSlash(filepath.Join("sha256", digest[:2], digest))
	finalDir := filepath.Join(cfg.UploadDir, "sha256", digest[:2])
	finalPath := filepath.Join(finalDir, digest)
	if err := os.MkdirAll(finalDir, 0700); err != nil {
		return StoredAttachment{}, fmt.Errorf("create immutable upload directory: %w", err)
	}
	if info, err := os.Lstat(finalPath); err == nil {
		if !info.Mode().IsRegular() {
			return StoredAttachment{}, fmt.Errorf("immutable upload path is not a regular file")
		}
		if err := verifyStoredUpload(finalPath, digest, written); err != nil {
			return StoredAttachment{}, err
		}
		if err := os.Chmod(finalPath, 0440); err != nil {
			return StoredAttachment{}, fmt.Errorf("protect immutable upload: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return StoredAttachment{}, fmt.Errorf("inspect retained upload: %w", err)
	} else {
		if err := os.Rename(temporaryPath, finalPath); err != nil {
			return StoredAttachment{}, fmt.Errorf("commit immutable upload: %w", err)
		}
		keepTemporary = true
		if err := os.Chmod(finalPath, 0440); err != nil {
			return StoredAttachment{}, fmt.Errorf("protect immutable upload: %w", err)
		}
	}
	return StoredAttachment{
		DriveFileID: reference.FileID, SourceURL: truncateUTF8(reference.SourceURL, 2000),
		OriginalName: originalName,
		ContentType:  contentType, SizeBytes: written, SHA256: digest, StorageKey: storageKey,
	}, nil
}

func copyUploadWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func verifyStoredUpload(path, expectedSHA string, expectedSize int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open retained upload: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return fmt.Errorf("verify retained upload: %w", err)
	}
	if size != expectedSize || hex.EncodeToString(hasher.Sum(nil)) != expectedSHA {
		return fmt.Errorf("immutable upload content verification failed")
	}
	return nil
}

func normalizeContentType(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil || !strings.Contains(mediaType, "/") {
		return "", fmt.Errorf("content type %q is invalid", value)
	}
	return strings.ToLower(mediaType), nil
}

func contentTypeAllowed(allowed []string, actual string) bool {
	for _, value := range allowed {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == actual || (strings.HasSuffix(normalized, "/*") && strings.HasPrefix(actual, strings.TrimSuffix(normalized, "*"))) {
			return true
		}
	}
	return false
}

func truncateUTF8(value string, max int) string {
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}
