// Package gaonfake is a stand-in for gaon's resource API, for the
// operator's tests. It serves the paths of three kinds, MicroVM, Secret
// and Volume, over a map, with the compare-and-swap writes and the
// label selector of a list, and validates every write with gaon's own
// types.
//
// It does not prove gaon's server: it has no RBAC (so a label-limited
// ClusterRole is never refused), no ownerReferences garbage collection,
// no Node authorizer, no binding and no bindingEpoch handling beyond
// what a test writes into a status, no watch, and it does not refuse a
// get by label as the real server does. gaon's `lib/api` ships its own
// in-memory server only as a `_test.go` file, which another module
// cannot import.
package gaonfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/garamsh/gaon/lib/api"
)

// Request is one request the fake served.
type Request struct {
	Method string
	Kind   string
	Name   string
}

// Server is the fake. Its methods are safe for concurrent use.
type Server struct {
	mu       sync.Mutex
	rev      int64
	objects  map[string]map[string][]byte // kind -> name -> object
	requests []Request
	url      string
}

// New starts a fake and returns it with a client for it. The client
// carries the selector, as the operator's does.
func New(t *testing.T, selector api.Selector) (*Server, *api.Client) {
	t.Helper()
	s := NewServer()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	s.url = ts.URL
	return s, api.NewClient(ts.URL, ts.Client(), api.WithLabelSelector(selector))
}

// NewServer returns a fake that is not listening. Its Handler is what
// a command serves on a port of its own (tests/gaonstandin).
func NewServer() *Server {
	return &Server{objects: map[string]map[string][]byte{"microvms": {}, "secrets": {}, "volumes": {}}}
}

// Handler serves the three kinds' paths.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for kind := range s.objects {
		mux.HandleFunc("PUT /"+kind+"/{name}", s.put(kind))
		mux.HandleFunc("DELETE /"+kind+"/{name}", s.remove(kind))
		mux.HandleFunc("GET /"+kind+"/{name}", s.get(kind))
		mux.HandleFunc("GET /"+kind, s.list(kind))
	}
	return mux
}

// URL is the address the fake serves at.
func (s *Server) URL() string { return s.url }

// Requests returns every request served so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Count returns how many requests of method for kind were served.
func (s *Server) Count(method, kind string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.Method == method && r.Kind == kind {
			n++
		}
	}
	return n
}

// MicroVM returns the stored MicroVM and whether it exists.
func (s *Server) MicroVM(t *testing.T, name string) (api.MicroVM, bool) {
	t.Helper()
	return load[api.MicroVM](t, s, "microvms", name)
}

// Secret returns the stored Secret and whether it exists.
func (s *Server) Secret(t *testing.T, name string) (api.Secret, bool) {
	t.Helper()
	return load[api.Secret](t, s, "secrets", name)
}

// Volume returns the stored Volume and whether it exists.
func (s *Server) Volume(t *testing.T, name string) (api.Volume, bool) {
	t.Helper()
	return load[api.Volume](t, s, "volumes", name)
}

// SetStatus writes status as the Node Agent would, past the validation
// a client's write goes through.
func (s *Server) SetStatus(t *testing.T, name string, status api.MicroVMStatus) {
	t.Helper()
	if err := s.WriteStatus(name, status); err != nil {
		t.Fatalf("gaonfake: %v", err)
	}
}

// WriteStatus is SetStatus for a caller with no *testing.T: it answers
// an error where the MicroVM does not exist.
func (s *Server) WriteStatus(name string, status api.MicroVMStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.objects["microvms"][name]
	if !ok {
		return fmt.Errorf("no microvm %q to set the status of", name)
	}
	var m api.MicroVM
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("decode microvm %s: %w", name, err)
	}
	m.Status = status
	s.store("microvms", name, &m, &m.Metadata)
	return nil
}

// RemoveVolume removes a Volume as a person would, outside the
// requests the operator made.
func (s *Server) RemoveVolume(t *testing.T, name string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects["volumes"][name]; !ok {
		t.Fatalf("gaonfake: no volume %q to remove", name)
	}
	delete(s.objects["volumes"], name)
}

func load[T any](t *testing.T, s *Server, kind, name string) (T, bool) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out T
	raw, ok := s.objects[kind][name]
	if !ok {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("gaonfake: decode %s %s: %v", kind, name, err)
	}
	return out, true
}

// store stamps the next resource version on meta and keeps obj.
func (s *Server) store(kind, name string, obj any, meta *api.ObjectMeta) {
	s.rev++
	meta.ResourceVersion = api.ResourceVersion(strconv.FormatInt(s.rev, 10))
	raw, err := json.Marshal(obj)
	if err != nil {
		panic("gaonfake: encode: " + err.Error())
	}
	s.objects[kind][name] = raw
}

func (s *Server) record(r *http.Request, kind string) {
	s.requests = append(s.requests, Request{Method: r.Method, Kind: kind, Name: r.PathValue("name")})
}

func (s *Server) put(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.record(r, kind)
		var (
			obj   any
			meta  *api.ObjectMeta
			check func(old []byte) error
		)
		switch kind {
		case "microvms":
			var m api.MicroVM
			obj, meta = &m, &m.Metadata
			check = func(old []byte) error {
				if old == nil {
					return m.Validate()
				}
				var prev api.MicroVM
				if err := json.Unmarshal(old, &prev); err != nil {
					return err
				}
				if err := m.ValidateUpdate(&prev); err != nil {
					return err
				}
				m.Status = prev.Status
				return nil
			}
		case "secrets":
			var o api.Secret
			obj, meta = &o, &o.Metadata
			check = func([]byte) error { return o.Validate() }
		default:
			var v api.Volume
			obj, meta = &v, &v.Metadata
			check = func(old []byte) error {
				if old == nil {
					return v.ValidateCreate()
				}
				var prev api.Volume
				if err := json.Unmarshal(old, &prev); err != nil {
					return err
				}
				return v.ValidateUpdate(&prev)
			}
		}
		if err := json.NewDecoder(r.Body).Decode(obj); err != nil {
			writeError(w, api.NewAPIError(api.ReasonInvalid, "decode: %v", err))
			return
		}
		name := r.PathValue("name")
		if meta.Name != name {
			writeError(w, api.NewAPIError(api.ReasonInvalid, "metadata.name %q is not the path's %q", meta.Name, name))
			return
		}
		old, found := s.objects[kind][name]
		if !found {
			old = nil
		}
		if err := s.checkVersion(old, meta.ResourceVersion, name); err != nil {
			writeError(w, err)
			return
		}
		if err := check(old); err != nil {
			writeError(w, err)
			return
		}
		// The server sets a UID on create and keeps it after, as gaon
		// does; a UID the client sent is not trusted.
		meta.UID = api.NewUID()
		if old != nil {
			var stored struct {
				Metadata api.ObjectMeta `json:"metadata"`
			}
			if err := json.Unmarshal(old, &stored); err != nil {
				writeError(w, err)
				return
			}
			meta.UID = stored.Metadata.UID
		}
		s.store(kind, name, obj, meta)
		writeJSON(w, http.StatusOK, obj)
	}
}

// checkVersion is the compare-and-swap: an empty version creates and
// refuses an object that exists, any other must be the stored one.
func (s *Server) checkVersion(old []byte, given api.ResourceVersion, name string) error {
	if old == nil {
		if given != "" {
			return api.NewAPIError(api.ReasonNotFound, "%s does not exist", name)
		}
		return nil
	}
	if given == "" {
		return api.NewAPIError(api.ReasonAlreadyExists, "%s already exists", name)
	}
	var stored struct {
		Metadata api.ObjectMeta `json:"metadata"`
	}
	if err := json.Unmarshal(old, &stored); err != nil {
		return err
	}
	if stored.Metadata.ResourceVersion != given {
		return api.NewAPIError(api.ReasonConflict, "%s is at version %s, not %s",
			name, stored.Metadata.ResourceVersion, given)
	}
	return nil
}

func (s *Server) remove(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.record(r, kind)
		name := r.PathValue("name")
		if _, ok := s.objects[kind][name]; !ok {
			writeError(w, api.NewAPIError(api.ReasonNotFound, "%s does not exist", name))
			return
		}
		delete(s.objects[kind], name)
		writeJSON(w, http.StatusOK, map[string]string{})
	}
}

func (s *Server) get(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.record(r, kind)
		raw, ok := s.objects[kind][r.PathValue("name")]
		if !ok {
			writeError(w, api.NewAPIError(api.ReasonNotFound, "not found"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}
}

func (s *Server) list(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.record(r, kind)
		sel, err := api.ParseSelector(r.URL.Query().Get(api.LabelSelectorParam))
		if err != nil {
			writeError(w, err)
			return
		}
		items := []json.RawMessage{}
		for _, raw := range s.objects[kind] {
			var o struct {
				Metadata api.ObjectMeta `json:"metadata"`
			}
			if err := json.Unmarshal(raw, &o); err != nil {
				writeError(w, err)
				return
			}
			if sel.Matches(o.Metadata.Labels) {
				items = append(items, raw)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"metadata": api.ListMeta{ResourceVersion: api.ResourceVersion(strconv.FormatInt(s.rev, 10))},
			"items":    items,
		})
	}
}

func writeError(w http.ResponseWriter, err error) {
	apiErr, ok := err.(*api.APIError)
	if !ok {
		apiErr = api.NewAPIError(api.ReasonInternalError, "%v", err)
	}
	writeJSON(w, apiErr.Code, apiErr)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
