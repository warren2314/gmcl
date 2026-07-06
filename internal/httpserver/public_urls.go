package httpserver

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

const defaultPublicBaseURL = "https://gmcl.co.uk"

func publicBaseURL(r *http.Request) string {
	if base := configuredPublicBaseURL(); base != "" {
		return base
	}

	host := firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" || isInternalPublicHost(host) {
		return defaultPublicBaseURL
	}

	scheme := firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))
	switch {
	case isGMCLPublicHost(host):
		scheme = "https"
	case scheme == "http" && isLocalPublicHost(host):
		scheme = "http"
	case scheme == "http" || scheme == "https":
		scheme = "https"
	default:
		if isLocalPublicHost(host) {
			scheme = "http"
		} else {
			scheme = "https"
		}
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

func isGMCLPublicHost(host string) bool {
	name := strings.ToLower(hostnameOnly(host))
	return name == "gmcl.co.uk" || name == "www.gmcl.co.uk"
}

func isLocalPublicHost(host string) bool {
	name := strings.ToLower(hostnameOnly(host))
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}

func isInternalPublicHost(host string) bool {
	name := strings.ToLower(hostnameOnly(host))
	return name == "" || name == "app" || name == "internal"
}

func hostnameOnly(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end >= 0 {
			return strings.Trim(host[1:end], "[]")
		}
	}
	if strings.Count(host, ":") > 1 {
		return strings.Trim(host, "[]")
	}
	if before, _, ok := strings.Cut(host, ":"); ok {
		return before
	}
	return host
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

func magicAccessURL(r *http.Request) string {
	return publicBaseURL(r) + "/access"
}

func magicLinkEmailBlock(r *http.Request, token string) string {
	primary := magicLinkURL(r, token)
	alternate := alternateMagicLinkURL(r, token)
	accessURL := magicAccessURL(r)
	accessHelp := "\n\nIf the button will not open, type " + accessURL + " into your browser and paste this access code:\nACCESS_CODE:" + token
	if alternate == "" || alternate == primary {
		return "BUTTON_URL:" + primary + accessHelp + "\nACCESS_URL:" + accessURL
	}
	return "BUTTON_URL:" + primary + "\n\nIf your browser says \"This site can't be reached\", use this backup link:\nBACKUP_URL:" + alternate + accessHelp + "\nACCESS_URL:" + accessURL
}
