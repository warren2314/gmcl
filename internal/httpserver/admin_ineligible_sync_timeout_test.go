package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appmiddleware "cricket-ground-feedback/internal/middleware"
)

type writeDeadlineRecorder struct {
	header   http.Header
	deadline time.Time
}

func (w *writeDeadlineRecorder) Header() http.Header {
	return w.header
}

func (w *writeDeadlineRecorder) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (w *writeDeadlineRecorder) WriteHeader(int) {}

func (w *writeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestIneligibleSyncWriteDeadlineSurvivesLoggerWrapper(t *testing.T) {
	now := time.Date(2026, time.August, 11, 15, 0, 0, 0, time.UTC)
	recorder := &writeDeadlineRecorder{header: make(http.Header)}
	var deadlineErr error

	handler := appmiddleware.Logger()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deadlineErr = setIneligibleSyncWriteDeadline(w, now)
	}))
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/ineligible/sync", nil))

	if deadlineErr != nil {
		t.Fatalf("set write deadline through logger: %v", deadlineErr)
	}
	want := now.Add(ineligibleManualSyncTimeout + ineligibleManualSyncWriteGrace)
	if !recorder.deadline.Equal(want) {
		t.Fatalf("write deadline = %s, want %s", recorder.deadline, want)
	}
	if got := recorder.deadline.Sub(now); got <= 45*time.Second {
		t.Fatalf("write deadline extension = %s, must exceed the server's 45s default", got)
	}
}

func TestIneligibleSyncWriteDeadlineOverridesServerWriteTimeout(t *testing.T) {
	const serverWriteTimeout = 50 * time.Millisecond

	writeResult := make(chan error, 1)
	handler := appmiddleware.Logger()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := setIneligibleSyncWriteDeadline(w, time.Now()); err != nil {
			writeResult <- err
			return
		}
		time.Sleep(3 * serverWriteTimeout)
		_, err := io.WriteString(w, "import finished")
		writeResult <- err
	}))

	server := httptest.NewUnstartedServer(handler)
	server.Config.WriteTimeout = serverWriteTimeout
	server.Start()
	defer server.Close()

	client := server.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request after original server write timeout: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response after original server write timeout: %v", err)
	}
	if err := <-writeResult; err != nil {
		t.Fatalf("handler write: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got, want := string(body), "import finished"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
