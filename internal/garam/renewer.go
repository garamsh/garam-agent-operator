package garam

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Renewer replaces the certificate this operator authenticates with before it
// expires, by presenting the one it currently holds.
//
// It runs beside the Poller as a second manager.Runnable rather than inside the
// poll: the two answer to different clocks — a poll is a minute and a renewal
// window is hours — and a call one of them makes to garam should not hold the
// other's up. Added to a manager it joins the leader-election group
// (sigs.k8s.io/controller-runtime@v0.24.1:pkg/manager/runnable_group.go:99),
// which is what keeps two processes from renewing one identity: garam renews
// the certificate it last issued and refuses every other, so the loser of such
// a race would hold one it can never renew again.
type Renewer struct {
	client   *Client
	store    CredentialStore
	interval time.Duration
}

// NewRenewer returns a Renewer asking garam for a successor every interval and
// writing what it answers through store.
//
// interval bounds two things at once. It is far below the window garam admits a
// renewal in, so that a listener unreachable for part of that window is still
// met; and it is far above the delay between a renewal being stored and the
// process reading it back, because a second attempt made before that read
// presents the certificate this one just replaced.
func NewRenewer(client *Client, store CredentialStore, interval time.Duration) *Renewer {
	return &Renewer{client: client, store: store, interval: interval}
}

// Start renews until ctx is cancelled, which is what makes a Renewer a
// manager.Runnable. It returns no error: an error here stops the manager, and a
// garam that cannot be reached is what the next attempt is for.
func (r *Renewer) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("garam").Info(
		"Renewing this operator's certificate before it expires", "interval", r.interval)
	wait.UntilWithContext(ctx, r.renew, r.interval)
	return nil
}

// renew asks garam for a successor once and stores what it answers.
//
// An unrenewed certificate stops this operator at the handshake and stops
// nothing here: the poll goes on failing against garam and saying why, which is
// louder than a poller that quietly stopped and says nothing.
func (r *Renewer) renew(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("garam")

	credential, err := r.client.RenewIdentity(ctx)
	switch {
	case errors.Is(err, ErrRenewalTooEarly):
		// Not a failure. The window opens off the certificate's own validity,
		// and asking is how this operator learns that it has.
		log.V(1).Info("Renewed nothing: garam admits no renewal of this certificate yet")
		return
	case errors.Is(err, ErrCredentialSuperseded):
		log.Error(err, "Cannot renew this operator's certificate. It authenticates until it expires "+
			"and no retry recovers the lineage: mint one out of band")
		return
	case err != nil:
		log.Error(err, "Failed to renew this operator's certificate")
		return
	}

	if err := r.store.ReplaceCredential(ctx, credential); err != nil {
		log.Error(err, "Lost a certificate garam issued: it exists nowhere else and the one it "+
			"replaced can no longer renew, so mint one out of band")
		return
	}
	log.Info("Renewed this operator's certificate")
}
