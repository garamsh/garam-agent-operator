package certificate

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"
)

// Reloader serves the key pair in two files, reading them again whenever either changes.
type Reloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	current *tls.Certificate
	stamp   [2]time.Time
	// lastErr is why the files last failed to load; the pair loaded before it is still served.
	lastErr error
}

// NewReloader loads the key pair in certFile and keyFile, and refuses a pair that does not load.
func NewReloader(certFile, keyFile string) (*Reloader, error) {
	r := &Reloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// GetCertificate is tls.Config's GetCertificate. It serves the pair last loaded, after loading
// the files again if either one's modification time changed. A pair that fails to load leaves
// the one loaded before it in service.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stamp, err := r.stamps(); err == nil && stamp != r.stamp {
		r.lastErr = r.reloadLocked()
	}
	return r.current, nil
}

// Err is why the files last failed to load, or nil when the pair served is the files' current one.
func (r *Reloader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *Reloader) reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloadLocked()
}

func (r *Reloader) reloadLocked() error {
	stamp, err := r.stamps()
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load certificate: %w", err)
	}
	r.current, r.stamp = &pair, stamp
	return nil
}

func (r *Reloader) stamps() ([2]time.Time, error) {
	var stamp [2]time.Time
	for i, path := range []string{r.certFile, r.keyFile} {
		info, err := os.Stat(path)
		if err != nil {
			return stamp, fmt.Errorf("stat certificate file: %w", err)
		}
		stamp[i] = info.ModTime()
	}
	return stamp, nil
}
