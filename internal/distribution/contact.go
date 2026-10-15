package distribution

import (
	"context"
	"sync"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// contactInterval is the least time between two writes of one controller's last contact. The
// feed's long poll brings a controller back at least every 30 seconds, so a write per request
// would be one every few seconds per controller for a time a person reads in minutes (ADR 0074).
const contactInterval = time.Minute

// contacts coalesces the writes of each controller's last contact, in this process: a contact
// within contactInterval of the last one written is not written.
type contacts struct {
	now     func() time.Time
	mu      sync.Mutex
	written map[string]time.Time
}

func newContacts(now func() time.Time) *contacts {
	return &contacts{now: now, written: map[string]time.Time{}}
}

// record writes c's contact now, unless one within contactInterval was written. A write that
// fails is forgotten and returned, so the caller can log it and the next request writes again.
func (k *contacts) record(ctx context.Context, definitions definition.Service, c controller) error {
	at := k.now()
	k.mu.Lock()
	last, ok := k.written[c.grn]
	if ok && at.Sub(last) < contactInterval {
		k.mu.Unlock()
		return nil
	}
	k.written[c.grn] = at
	k.mu.Unlock()

	err := definitions.RecordOperatorContact(ctx, c.orgGRN, c.grn, at)
	if err != nil {
		k.mu.Lock()
		if k.written[c.grn].Equal(at) {
			delete(k.written, c.grn)
		}
		k.mu.Unlock()
	}
	return err
}
