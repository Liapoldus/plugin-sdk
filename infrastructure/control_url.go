package infrastructure

import "net/url"

func parseControlURL(raw string) (*url.URL, bool) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, false
	}
	return parsed, true
}

// controlEndpointURL resolves a contract path that carries no path parameter.
// The base URL is the only place a scheme, host or userinfo could come from, and
// parseControlURL has already refused one that is not a bare HTTPS origin, so a
// call can never be aimed at a path, a query or another origin.
func controlEndpointURL(base *url.URL, path string) (*url.URL, error) {
	if base == nil || base.Scheme != "https" || base.Host == "" || base.User != nil ||
		base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrInvalidControlCall
	}
	resolved, err := url.ParseRequestURI(path)
	if err != nil || resolved.Host != "" || resolved.Scheme != "" {
		return nil, ErrInvalidControlCall
	}
	target := *base
	target.Path = trimControlBasePath(base.Path) + resolved.Path
	target.RawPath = ""
	return &target, nil
}

func trimControlBasePath(path string) string {
	for len(path) > 0 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}
