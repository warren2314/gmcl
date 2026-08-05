package ineligible

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type driveFileMetadata struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     string `json:"size"`
	Trashed  bool   `json:"trashed"`
}

// DownloadUpload retrieves metadata and media for one ID that was parsed from
// the exact File Upload cell. It does not list or search Drive.
func (s *GoogleSheetsSource) DownloadUpload(ctx context.Context, fileID string, maxBytes int64) (UploadDownload, error) {
	if !driveFileIDPattern.MatchString(fileID) {
		return UploadDownload{}, fmt.Errorf("invalid Google Drive file ID")
	}
	if maxBytes < 1 {
		return UploadDownload{}, fmt.Errorf("Google Drive byte limit must be positive")
	}
	token, err := s.token(ctx)
	if err != nil {
		return UploadDownload{}, err
	}
	base := strings.TrimRight(s.cfg.DriveBaseURL, "/") + "/files/" + url.PathEscape(fileID)
	metadataURL, err := url.Parse(base)
	if err != nil {
		return UploadDownload{}, fmt.Errorf("build Google Drive metadata URL: %w", err)
	}
	query := metadataURL.Query()
	query.Set("fields", "id,name,mimeType,size,trashed")
	query.Set("supportsAllDrives", "true")
	metadataURL.RawQuery = query.Encode()
	metadataRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL.String(), nil)
	if err != nil {
		return UploadDownload{}, fmt.Errorf("create Google Drive metadata request: %w", err)
	}
	metadataRequest.Header.Set("Accept", "application/json")
	metadataRequest.Header.Set("Authorization", "Bearer "+token)
	metadataResponse, err := doGoogleRequest(ctx, s.httpClient, metadataRequest)
	if err != nil {
		return UploadDownload{}, fmt.Errorf("Google Drive metadata request: %w", err)
	}
	metadataBody, readErr := io.ReadAll(io.LimitReader(metadataResponse.Body, 1<<20))
	metadataResponse.Body.Close()
	if readErr != nil {
		return UploadDownload{}, fmt.Errorf("read Google Drive metadata response: %w", readErr)
	}
	if metadataResponse.StatusCode < 200 || metadataResponse.StatusCode >= 300 {
		return UploadDownload{}, fmt.Errorf("Google Drive metadata HTTP %d: %s", metadataResponse.StatusCode, truncateForError(metadataBody, 500))
	}
	var metadata driveFileMetadata
	if err := json.Unmarshal(metadataBody, &metadata); err != nil {
		return UploadDownload{}, fmt.Errorf("decode Google Drive metadata response: %w", err)
	}
	if metadata.ID != fileID || metadata.Trashed || strings.TrimSpace(metadata.Name) == "" {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s metadata is invalid or unavailable", fileID)
	}
	if strings.HasPrefix(strings.ToLower(metadata.MimeType), "application/vnd.google-apps.") {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s is not a binary Form upload", fileID)
	}
	normalizedMetadataType, err := normalizeContentType(metadata.MimeType)
	if err != nil {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s metadata content type is invalid", fileID)
	}
	if !contentTypeAllowed(s.cfg.UploadContentTypes, normalizedMetadataType) {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s content type %q is not permitted", fileID, normalizedMetadataType)
	}
	size, err := strconv.ParseInt(metadata.Size, 10, 64)
	if err != nil || size < 1 {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s has no valid binary size", fileID)
	}
	if size > maxBytes {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s exceeds the configured byte limit", fileID)
	}

	mediaURL, err := url.Parse(base)
	if err != nil {
		return UploadDownload{}, fmt.Errorf("build Google Drive media URL: %w", err)
	}
	query = mediaURL.Query()
	query.Set("alt", "media")
	query.Set("supportsAllDrives", "true")
	mediaURL.RawQuery = query.Encode()
	mediaRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL.String(), nil)
	if err != nil {
		return UploadDownload{}, fmt.Errorf("create Google Drive media request: %w", err)
	}
	mediaRequest.Header.Set("Accept", metadata.MimeType)
	mediaRequest.Header.Set("Authorization", "Bearer "+token)
	mediaResponse, err := doGoogleRequest(ctx, s.httpClient, mediaRequest)
	if err != nil {
		return UploadDownload{}, fmt.Errorf("Google Drive media request: %w", err)
	}
	if mediaResponse.StatusCode < 200 || mediaResponse.StatusCode >= 300 {
		defer mediaResponse.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(mediaResponse.Body, 64<<10))
		return UploadDownload{}, fmt.Errorf("Google Drive media HTTP %d: %s", mediaResponse.StatusCode, truncateForError(body, 500))
	}
	if mediaResponse.ContentLength > maxBytes {
		mediaResponse.Body.Close()
		return UploadDownload{}, fmt.Errorf("Google Drive file %s exceeds the configured byte limit", fileID)
	}
	if responseType := strings.TrimSpace(mediaResponse.Header.Get("Content-Type")); responseType != "" {
		normalizedResponse, responseErr := normalizeContentType(responseType)
		if responseErr != nil || (normalizedResponse != "application/octet-stream" && normalizedResponse != normalizedMetadataType) {
			mediaResponse.Body.Close()
			return UploadDownload{}, fmt.Errorf("Google Drive file %s media content type did not match metadata", fileID)
		}
	}
	return UploadDownload{
		DriveFileID: fileID, Name: metadata.Name, ContentType: metadata.MimeType,
		Size: size, Body: mediaResponse.Body,
	}, nil
}
