package middleware

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFMiddlewareIssuesHardenedCookieAndContextToken(t *testing.T) {
	var contextualToken string
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextualToken = CSRFToken(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/portal/contexts", nil),
	)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != CSRFCookieName ||
		cookie.Path != "/" ||
		!cookie.Secure ||
		!cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("CSRF cookie is not hardened: %#v", cookie)
	}
	if contextualToken == "" || contextualToken != cookie.Value {
		t.Fatalf("context token = %q, cookie = %q", contextualToken, cookie.Value)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32 {
		t.Fatalf("CSRF token is not 256 random bits: length=%d, error=%v", len(raw), err)
	}
}

func TestCSRFMiddlewareAcceptsMatchingFormAndHeaderTokens(t *testing.T) {
	token, err := generateCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		request func() *http.Request
	}{
		{
			name: "form",
			request: func() *http.Request {
				values := url.Values{"csrf_token": {token}}
				request := httptest.NewRequest(
					http.MethodPost,
					"/portal/contexts",
					strings.NewReader(values.Encode()),
				)
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return request
			},
		},
		{
			name: "header",
			request: func() *http.Request {
				request := httptest.NewRequest(
					http.MethodPost,
					"/portal/sessions/revoke-all",
					nil,
				)
				request.Header.Set("X-CSRF-Token", token)
				return request
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := CSRFMiddleware(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					called = true
					w.WriteHeader(http.StatusNoContent)
				},
			))
			request := test.request()
			request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent || !called {
				t.Fatalf("status = %d, called = %t", recorder.Code, called)
			}
			if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
				t.Fatalf("valid request unexpectedly rotated cookie: %#v", cookies)
			}
		})
	}
}

func TestCSRFMiddlewareRejectsMissingAndMismatchedTokens(t *testing.T) {
	token, err := generateCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		cookie string
		form   string
		header string
	}{
		{name: "missing request token", cookie: token},
		{name: "missing cookie", form: token},
		{name: "mismatched form", cookie: token, form: token + "x"},
		{name: "mismatched header", cookie: token, header: "not-the-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			values := url.Values{}
			if test.form != "" {
				values.Set("csrf_token", test.form)
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/portal/contexts",
				strings.NewReader(values.Encode()),
			)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.header != "" {
				request.Header.Set("X-CSRF-Token", test.header)
			}
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{
					Name:  CSRFCookieName,
					Value: test.cookie,
				})
			}
			recorder := httptest.NewRecorder()
			CSRFMiddleware(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					called = true
					w.WriteHeader(http.StatusNoContent)
				},
			)).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden || called {
				t.Fatalf("status = %d, called = %t", recorder.Code, called)
			}
		})
	}
}

func TestCSRFTokenEqualRejectsEmptyAndDifferentLengths(t *testing.T) {
	if !csrfTokenEqual("same-token", "same-token") {
		t.Fatal("matching token rejected")
	}
	for _, pair := range [][2]string{
		{"", ""},
		{"token", ""},
		{"", "token"},
		{"token", "different"},
		{"token", "token-longer"},
	} {
		if csrfTokenEqual(pair[0], pair[1]) {
			t.Fatalf("unexpected token match: %q, %q", pair[0], pair[1])
		}
	}
}
