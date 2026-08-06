package ineligible

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoogleDriveDownloadUsesMetadataThenBoundedMedia(t *testing.T) {
	const fileID = "drive-file-1234567890"
	content := "immutable upload bytes"
	var metadataCalls atomic.Int32
	var mediaCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive/v3/files/"+fileID {
			t.Errorf("path: %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cached-test-token" {
			t.Errorf("authorization: %q", got)
		}
		if r.URL.Query().Get("supportsAllDrives") != "true" {
			t.Errorf("supportsAllDrives missing")
		}
		if r.URL.Query().Get("alt") == "media" {
			mediaCalls.Add(1)
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Length", "22")
			_, _ = w.Write([]byte(content))
			return
		}
		metadataCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + fileID + `","name":"evidence.pdf","mimeType":"application/pdf","size":"22","trashed":false}`))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.DriveBaseURL = server.URL + "/drive/v3"
	source := &GoogleSheetsSource{
		cfg: cfg, httpClient: server.Client(), accessToken: "cached-test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	download, err := source.DownloadUpload(context.Background(), fileID, 1024)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer download.Body.Close()
	got, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatalf("read media: %v", err)
	}
	if string(got) != content || download.Name != "evidence.pdf" || download.ContentType != "application/pdf" || download.Size != 22 {
		t.Fatalf("download=%+v body=%q", download, got)
	}
	if metadataCalls.Load() != 1 || mediaCalls.Load() != 1 {
		t.Fatalf("metadata=%d media=%d", metadataCalls.Load(), mediaCalls.Load())
	}
}

func TestGoogleDriveDownloadAcceptsMP4Evidence(t *testing.T) {
	const fileID = "drive-video-1234567890"
	content := []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "24")
			_, _ = w.Write(content)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + fileID + `","name":"evidence.mp4","mimeType":"video/mp4","size":"24","trashed":false}`))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.DriveBaseURL = server.URL
	source := &GoogleSheetsSource{
		cfg: cfg, httpClient: server.Client(), accessToken: "cached-test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	download, err := source.DownloadUpload(context.Background(), fileID, 1024)
	if err != nil {
		t.Fatalf("download MP4: %v", err)
	}
	defer download.Body.Close()
	got, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) || download.Name != "evidence.mp4" || download.ContentType != "video/mp4" || download.Size != 24 {
		t.Fatalf("download=%+v body=%x", download, got)
	}
}

func TestGoogleDriveDownloadRejectsMetadataOverLimitWithoutMediaRequest(t *testing.T) {
	const fileID = "drive-file-1234567890"
	var mediaCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			mediaCalls.Add(1)
			t.Error("media endpoint must not be called for oversized metadata")
			http.Error(w, "unexpected media request", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + fileID + `","name":"large.pdf","mimeType":"application/pdf","size":"2048","trashed":false}`))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.DriveBaseURL = server.URL
	source := &GoogleSheetsSource{
		cfg: cfg, httpClient: server.Client(), accessToken: "cached-test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	_, err := source.DownloadUpload(context.Background(), fileID, 1024)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
	if mediaCalls.Load() != 0 {
		t.Fatalf("media calls: %d", mediaCalls.Load())
	}
}

func TestGoogleDriveDownloadRejectsDisallowedMetadataWithoutMediaRequest(t *testing.T) {
	const fileID = "drive-file-1234567890"
	var mediaCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			mediaCalls.Add(1)
			http.Error(w, "unexpected media request", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + fileID + `","name":"payload.exe","mimeType":"application/x-msdownload","size":"2","trashed":false}`))
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.DriveBaseURL = server.URL
	source := &GoogleSheetsSource{
		cfg: cfg, httpClient: server.Client(), accessToken: "cached-test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	_, err := source.DownloadUpload(context.Background(), fileID, 1024)
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("expected content-type error, got %v", err)
	}
	if mediaCalls.Load() != 0 {
		t.Fatalf("media calls: %d", mediaCalls.Load())
	}
}
