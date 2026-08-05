package ineligible

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreUploadProtectsNewAndReusedContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits consistently")
	}
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	reference := uploadReference{FileID: "drive-file-1234567890", SourceURL: "https://drive.google.com/open?id=drive-file-1234567890"}
	content := []byte("immutable evidence")
	download := func() UploadDownload {
		return UploadDownload{
			DriveFileID: reference.FileID,
			Name:        "evidence.pdf",
			ContentType: "application/pdf",
			Size:        int64(len(content)),
			Body:        io.NopCloser(bytes.NewReader(content)),
		}
	}

	first, err := storeUpload(context.Background(), cfg, reference, download(), 1<<20)
	if err != nil {
		t.Fatalf("store new upload: %v", err)
	}
	retainedPath := filepath.Join(cfg.UploadDir, filepath.FromSlash(first.StorageKey))
	assertFileMode(t, retainedPath, 0400)

	if err := os.Chmod(retainedPath, 0640); err != nil {
		t.Fatalf("widen retained upload mode for reuse test: %v", err)
	}
	second, err := storeUpload(context.Background(), cfg, reference, download(), 1<<20)
	if err != nil {
		t.Fatalf("reuse retained upload: %v", err)
	}
	if second.StorageKey != first.StorageKey {
		t.Fatalf("reused storage key = %q, want %q", second.StorageKey, first.StorageKey)
	}
	assertFileMode(t, retainedPath, 0400)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("file mode = %#o, want %#o", got, want)
	}
}
