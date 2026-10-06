package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

// noRedirects answers 404 wherever the routes beneath would redirect. Go's ServeMux redirects a
// path it would clean, and a subtree's path without its trailing slash, and a redirect changes the
// request target an operation authority binds (#220): the console treats one as an error and
// follows none. So no route here issues one, and what would have been redirected names no route.
func noRedirects(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&redirectRefuser{ResponseWriter: w}, r)
	})
}

// redirectRefuser turns a redirect written through it into a 404 with no Location.
type redirectRefuser struct {
	http.ResponseWriter
	refused bool
}

func (w *redirectRefuser) WriteHeader(status int) {
	if status >= http.StatusMultipleChoices && status < http.StatusBadRequest {
		w.refused = true
		h := w.Header()
		h.Del("Location")
		h.Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w.ResponseWriter).Encode(map[string]string{"message": "not found"})
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *redirectRefuser) Write(b []byte) (int, error) {
	if w.refused {
		// The redirect's own body is dropped; the 404's was written in its place.
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// originHost is a host and optional port as a browser serializes one in Origin: lowercase, and no
// wildcard, which a registration may not use (#220).
var originHost = regexp.MustCompile(`^([a-z0-9.-]+|\[[0-9a-f:.]+\])(:[0-9]{1,5})?$`)

// consoleOrigin reads one registered console origin: an http or https origin with a host and no
// credentials, path, query or fragment, in the form a browser sends in Origin, which is the form it
// is compared in.
func consoleOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !originHost.MatchString(u.Host) || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != value {
		return "", fmt.Errorf("%q is not an origin: want scheme://host[:port] with nothing after it", value)
	}
	return value, nil
}
