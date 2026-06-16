package httpserver

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

func publicBaseURL(r *http.Request) string {
	if base := configuredPublicBaseURL(); base != "" {
		return base
	}

	scheme := firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}

	host := firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		host = "gmcl.co.uk"
	}

	return scheme + "://" + host
}

func firstForwardedValue(value string) string {
	value = strings.TrimSpace(value)
	if before, _, found := strings.Cut(value, ","); found {
		return strings.TrimSpace(before)
	}
	return value
}

func configuredPublicBaseURL() string {
	for _, key := range []string{"PUBLIC_BASE_URL", "APP_BASE_URL"} {
		if base := strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/"); base != "" {
			return base
		}
	}
	return ""
}

func publicAlternateBaseURL(r *http.Request) string {
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_ALT_BASE_URL")), "/"); base != "" {
		return base
	}

	primary := publicBaseURL(r)
	u, err := url.Parse(primary)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "gmcl.co.uk":
		u.Host = replaceURLHost(u.Host, "www.gmcl.co.uk")
	case "www.gmcl.co.uk":
		u.Host = replaceURLHost(u.Host, "gmcl.co.uk")
	default:
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

func replaceURLHost(originalHost, replacementName string) string {
	if strings.Contains(originalHost, ":") {
		if _, port, ok := strings.Cut(originalHost, ":"); ok && port != "" {
			return replacementName + ":" + port
		}
	}
	return replacementName
}

// ligaturePathReplacer maps Unicode typographic ligatures back to their ASCII
// equivalents. Some clients (PDF copy, iOS "smart" text) rewrite the "fi" in
// "confirm" to the U+FB01 ligature, producing "/magic-link/conﬁrm" which no
// route matches. We apply this only to the request path (never the token query)
// and only on otherwise-404 requests, so canonical traffic is unaffected.
var ligaturePathReplacer = strings.NewReplacer(
	"ﬀ", "ff",
	"ﬁ", "fi",
	"ﬂ", "fl",
	"ﬃ", "ffi",
	"ﬄ", "ffl",
	"ﬅ", "st",
	"ﬆ", "st",
)

// canonicalizePath returns the path with typographic ligatures normalised to
// ASCII. The returned value equals the input when nothing changed.
func canonicalizePath(path string) string {
	return ligaturePathReplacer.Replace(path)
}

func magicLinkURL(r *http.Request, token string) string {
	return publicBaseURL(r) + "/magic-link/confirm?token=" + url.QueryEscape(token)
}

func alternateMagicLinkURL(r *http.Request, token string) string {
	base := publicAlternateBaseURL(r)
	if base == "" || base == publicBaseURL(r) {
		return ""
	}
	return base + "/magic-link/confirm?token=" + url.QueryEscape(token)
}

func magicLinkEmailBlock(r *http.Request, token string) string {
	primary := magicLinkURL(r, token)
	alternate := alternateMagicLinkURL(r, token)
	if alternate == "" || alternate == primary {
		return primary
	}
	return primary + "\n\nIf your browser says \"This site can't be reached\", use this backup link:\nBACKUP_URL:" + alternate
}
