package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoggerPreservesFlusher(t *testing.T) {
	handler := Logger()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("logger response writer does not preserve http.Flusher")
		}
		w.WriteHeader(http.StatusAccepted)
		flusher.Flush()
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/events", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d", recorder.Code)
	}
}
