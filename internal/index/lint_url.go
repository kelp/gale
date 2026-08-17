package index

import (
	"net/url"
	"strings"
)

func lintURL(issues *[]Issue, p, raw string) {
	if raw == "" {
		add(issues, p, "url is required")
		return
	}
	// Templates are checked on the raw string so a `{{` in the
	// authority is reported even when url.Parse rejects the host.
	lintURLTemplates(issues, p, raw)
	u, err := url.Parse(raw)
	if err != nil {
		add(issues, p, "url is malformed")
		return
	}
	lintURLParts(issues, p, u)
}

func lintURLParts(issues *[]Issue, p string, u *url.URL) {
	if u.Opaque != "" {
		add(issues, p, "url must not be opaque")
	}
	if u.Scheme != "https" {
		add(issues, p, "url must use https")
	}
	if u.User != nil {
		add(issues, p, "url must not contain userinfo")
	}
	if u.RawQuery != "" {
		add(issues, p, "url must not contain a query")
	}
	if u.Fragment != "" {
		add(issues, p, "url must not contain a fragment")
	}
	if u.Port() != "" {
		add(issues, p, "url must not contain a port")
	}
	if !AllowedHost(u.Hostname()) {
		add(issues, p, "url host is not allowed")
	}
}

func lintURLTemplates(issues *[]Issue, p, raw string) {
	if !strings.Contains(raw, "{{") && !strings.Contains(raw, "}}") {
		return
	}
	pathStart := templatePathStart(raw)
	if pathStart < 0 {
		add(issues, p, "url template must be in the path")
		return
	}
	if strings.Contains(raw[:pathStart], "{{") || strings.Contains(raw[:pathStart], "}}") {
		add(issues, p, "url template must be in the path")
		return
	}
	scanTemplates(issues, p, raw[pathStart:])
}

func templatePathStart(raw string) int {
	rest, ok := strings.CutPrefix(raw, "https://")
	if !ok {
		scheme := strings.Index(raw, "://")
		if scheme < 0 {
			return -1
		}
		rest = raw[scheme+3:]
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return -1
	}
	return len(raw) - len(rest) + slash
}

func scanTemplates(issues *[]Issue, p, path string) {
	i := 0
	for i < len(path) {
		switch {
		case strings.HasPrefix(path[i:], "{{"):
			end := strings.Index(path[i+2:], "}}")
			if end < 0 {
				add(issues, p, "url template is unbalanced")
				return
			}
			ident := path[i+2 : i+2+end]
			if !allowedTemplate[ident] {
				add(issues, p, "url template is not allowed")
			}
			i += 2 + end + 2
		case strings.HasPrefix(path[i:], "}}"):
			add(issues, p, "url template is unbalanced")
			return
		default:
			i++
		}
	}
}

var allowedTemplate = map[string]bool{
	"version": true,
	"os":      true,
	"arch":    true,
}
