package desired

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

const (
	// recoverPass is how often the recoverer looks for recoveries to take a step.
	recoverPass = time.Second

	// pemCertificate is the PEM block type of a certificate.
	pemCertificate = "CERTIFICATE"

	// preparedWait is how long a prepared recovery waits before it is asked
	// about again: until an administrator finalizes it, there is nothing new.
	preparedWait = 10 * time.Second
)

// Recovered is the control service's answer to a recovery's certificate
// request: the recovery it is for, and garam's recovered credential once the
// recovery is finalized: the lineage, the certificate, and the issuer and garam
// server root it was signed under. CertificatePEM is nil before that, and
// IssuerPEM and ServerRootPEM are nil where control answers none, which a
// control before ADR 0062 does.
type Recovered struct {
	Agent          string
	RequestID      string
	Epoch          string
	Lineage        string
	CertificatePEM []byte
	IssuerPEM      []byte
	ServerRootPEM  []byte
}

// RecoveryStore holds what the recoverer persists and reads.
type RecoveryStore interface {
	// Recovering lists the managed agents with a recovery request persisted.
	Recovering(ctx context.Context) ([]string, error)

	// LoadRecovery returns the recovery request persisted for agent, and false
	// where none is.
	LoadRecovery(ctx context.Context, agent string) (PendingRequest, bool, error)

	// SaveRecovery persists request for agent. A request already persisted is
	// kept, never replaced: its key is the one garam may have signed over.
	SaveRecovery(ctx context.Context, agent string, request PendingRequest) error

	// KeptIssuer returns the issuer of agent's placed credential, which a
	// recovered certificate answered with no chain is kept beside, and false
	// where no credential is placed.
	KeptIssuer(ctx context.Context, agent string) ([]byte, bool, error)

	// RefuseRecovery records on agent's persisted recovery request why its
	// recovered certificate was not placed, as a condition reason.
	RefuseRecovery(ctx context.Context, agent, reason string) error

	// PlaceRecovered writes the recovered certificate and the key it was signed
	// over into agent's credential, with the issuer and server root of
	// certificate where it names them and keeping the placed ones where it does
	// not, records the lineage on it, and then removes the persisted recovery
	// request. Where no credential is placed, it creates it whole, which needs
	// the chain named.
	PlaceRecovered(ctx context.Context, agent string, keyPEM []byte, certificate Certificate, lineage string) error
}

// Recoverer takes each managed agent's open credential recovery through this
// operator's half (#300, ADR 0059): a key and a certificate request, persisted
// before they are sent, then the recovered certificate, verified against the
// issuer kept from the first certificate before it is placed. It is a manager
// Runnable. Which recoveries are open it learns from the desired feed, through
// Offer; a persisted request is taken on whether the feed still names it, so
// a recovery finalized and no longer open is still collected.
type Recoverer struct {
	client *Client
	store  RecoveryStore

	// The waits, held here so a test in the package can shorten them.
	pass, transientFirst, transientLast, refusedWait, preparedWait time.Duration

	mu      sync.Mutex
	offered map[string]OpenRecovery

	attempts map[string]*attempt

	// placed is the recovery each agent's credential was last placed from. The
	// feed may still name it until its next answer, and a new request under its
	// id would be refused as finalized under another. It is held in memory: after
	// a restart the feed's first answer no longer names it.
	placed map[string]string
}

// NewRecoverer returns a Recoverer asking through client and persisting
// through store.
func NewRecoverer(client *Client, store RecoveryStore) *Recoverer {
	return &Recoverer{
		client: client, store: store,
		pass: recoverPass, transientFirst: transientFirst, transientLast: transientLast,
		refusedWait: refusedWait, preparedWait: preparedWait,
		offered: map[string]OpenRecovery{}, attempts: map[string]*attempt{}, placed: map[string]string{},
	}
}

// Offer replaces the open recoveries the recoverer knows of with those of one
// whole answer of the feed.
func (r *Recoverer) Offer(open map[string]OpenRecovery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offered = open
}

// Start recovers until ctx is cancelled. It returns no error, because an error
// from a Runnable stops the manager.
func (r *Recoverer) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("desired").Info("Recovering the credentials of managed agents")
	for ctx.Err() == nil {
		r.recoverAll(ctx)
		pause(ctx, r.pass)
	}

	return nil
}

// recoverAll takes a step for every agent with a persisted request or an open
// recovery.
func (r *Recoverer) recoverAll(ctx context.Context) {
	persisted, err := r.store.Recovering(ctx)
	if err != nil {
		logf.FromContext(ctx).WithName("desired").Error(err, "Failed to list the managed agents being recovered")

		return
	}
	r.mu.Lock()
	offered := r.offered
	r.mu.Unlock()
	agents := map[string]bool{}
	for _, agent := range persisted {
		agents[agent] = true
	}
	for agent := range offered {
		agents[agent] = true
	}
	for agent := range agents {
		open, isOpen := offered[agent]
		var opened *OpenRecovery
		if isOpen {
			opened = &open
		}
		r.recover(ctx, agent, opened)
	}
}

// recover takes one agent one step through its recovery. A request persisted
// is finished before another is begun, whatever the feed now names, because
// garam may have signed over its key.
func (r *Recoverer) recover(ctx context.Context, agent string, open *OpenRecovery) {
	log := logf.FromContext(ctx).WithName("desired").WithValues("agent", agent)
	state := r.attempts[agent]
	if state == nil {
		state = &attempt{}
		r.attempts[agent] = state
	}
	if time.Now().Before(state.next) {
		return
	}

	request, found, err := r.store.LoadRecovery(ctx, agent)
	if err != nil {
		log.Error(err, "Failed to read the persisted recovery request")
		r.retryTransient(state)

		return
	}
	if !found {
		if open == nil || r.placed[agent] == open.RequestID {
			delete(r.attempts, agent)

			return
		}
		if request, err = r.begin(ctx, agent, *open); err != nil {
			log.Error(err, "Failed to persist the recovery request")
			r.retryTransient(state)

			return
		}
		if request.ID == "" {
			return
		}
	}

	recovered, err := r.client.PrepareRecovery(ctx, agent, request)
	if refusal, refused := asRefusal(err); refused {
		state.backoff, state.next = 0, time.Now().Add(r.refusedWait)
		if kind := fmt.Sprintf("%d %s", refusal.Status, refusal.Kind); kind != state.refusedAs {
			log.Error(refusal, "The control service refused the recovery request; asking again slowly",
				"status", refusal.Status, "kind", refusal.Kind, "wait", r.refusedWait)
			state.refusedAs = kind
		}

		return
	}
	if err != nil {
		log.Info("The recovery request did not answer; asking again", "error", err.Error())
		r.retryTransient(state)

		return
	}
	if recovered.Agent != agent || recovered.RequestID != request.ID || recovered.Epoch != request.Epoch {
		log.Error(errors.New("the answer does not match the request"), "Not placing a recovery of another request",
			"answeredAgent", recovered.Agent, "answeredRequest", recovered.RequestID)
		state.next = time.Now().Add(r.refusedWait)

		return
	}
	if recovered.CertificatePEM == nil {
		// Prepared: an administrator's finalize is what comes next.
		state.backoff, state.next = 0, time.Now().Add(r.preparedWait)

		return
	}

	r.place(ctx, agent, request, recovered, state)
}

// begin persists a new key and request for the open recovery, under its
// request id and epoch, and returns it. An agent with no credential placed is
// begun too: garam answers a recovered certificate with its chain, which is
// what such an agent's credential is placed with (ADR 0062).
func (r *Recoverer) begin(ctx context.Context, agent string, open OpenRecovery) (PendingRequest, error) {
	request, err := newPendingRequest(open.Epoch)
	if err != nil {
		return PendingRequest{}, err
	}
	request.ID = open.RequestID
	if err := r.store.SaveRecovery(ctx, agent, request); err != nil {
		return PendingRequest{}, err
	}
	// What is sent is what was persisted, whoever persisted it.
	persisted, found, err := r.store.LoadRecovery(ctx, agent)
	if err != nil {
		return PendingRequest{}, err
	}
	if !found {
		return PendingRequest{}, errors.New("the recovery request was not persisted")
	}

	return persisted, nil
}

// place verifies the recovered certificate against the issuer it is to be
// written beside, and places it. That is the issuer control answered with it
// (ADR 0062), and the one kept from the agent's placed credential only where
// control answered none (ADR 0059). One that does not verify is not placed:
// the reason is recorded on the persisted request, which is kept.
func (r *Recoverer) place(ctx context.Context, agent string, request PendingRequest, recovered Recovered, state *attempt) {
	log := logf.FromContext(ctx).WithName("desired").WithValues("agent", agent)
	certificate := Certificate{
		Agent: agent, Epoch: recovered.Epoch, CertificatePEM: recovered.CertificatePEM,
		IssuerPEM: recovered.IssuerPEM, ServerRootPEM: recovered.ServerRootPEM,
	}
	issuer := recovered.IssuerPEM
	if len(recovered.IssuerPEM) == 0 || len(recovered.ServerRootPEM) == 0 {
		kept, placed, err := r.store.KeptIssuer(ctx, agent)
		if err != nil {
			log.Error(err, "Failed to read the kept issuer")
			r.retryTransient(state)

			return
		}
		if !placed {
			log.Error(errors.New("no chain is answered or placed"),
				"Not placing a recovered certificate with no chain to place it beside")
			state.next = time.Now().Add(r.refusedWait)

			return
		}
		issuer, certificate.IssuerPEM, certificate.ServerRootPEM = kept, nil, nil
	}
	if err := VerifyRecovered(agent, request.KeyPEM, recovered.CertificatePEM, issuer); err != nil {
		state.backoff, state.next = 0, time.Now().Add(r.refusedWait)
		if err := r.store.RefuseRecovery(ctx, agent, agentv1alpha1.ReasonRecoveredCertificateUnverified); err != nil {
			log.Error(err, "Failed to record why the recovered certificate was not placed")
		}
		if state.refusedAs != agentv1alpha1.ReasonRecoveredCertificateUnverified {
			log.Error(err, "Not placing a recovered certificate that does not verify against the issuer it would be placed beside",
				"lineage", recovered.Lineage)
			state.refusedAs = agentv1alpha1.ReasonRecoveredCertificateUnverified
		}

		return
	}
	if err := r.store.PlaceRecovered(ctx, agent, request.KeyPEM, certificate, recovered.Lineage); err != nil {
		log.Error(err, "Failed to place the recovered certificate")
		r.retryTransient(state)

		return
	}
	delete(r.attempts, agent)
	r.placed[agent] = request.ID
	log.Info("Placed the recovered credential of a managed agent", "lineage", recovered.Lineage)
}

// retryTransient schedules the next attempt after a wait doubling to the cap.
func (r *Recoverer) retryTransient(state *attempt) {
	if state.backoff == 0 {
		state.backoff = r.transientFirst
	} else {
		state.backoff = min(2*state.backoff, r.transientLast)
	}
	state.next = time.Now().Add(state.backoff)
}

// VerifyRecovered checks a recovered certificate before it is placed: one PEM
// certificate that chains to the issuer kept from the agent's first
// certificate, is signed over the persisted key, and names the agent's GRN as
// its one SAN URI. garam's contract names no issuer for a recovered certificate
// (garam@59fe68d api/machine.yaml RecoveredCredential), so the kept one is
// what is trusted, and a certificate that does not chain to it is never placed
// (ADR 0059).
func VerifyRecovered(agent string, keyPEM, certificatePEM, issuerPEM []byte) error {
	leaves := pemBlocks(certificatePEM)
	if len(leaves) != 1 {
		return errors.New("the recovered certificate is not one PEM certificate")
	}
	leaf, err := x509.ParseCertificate(leaves[0].Bytes)
	if err != nil {
		return fmt.Errorf("parse the recovered certificate: %w", err)
	}
	roots := x509.NewCertPool()
	for _, block := range pemBlocks(issuerPEM) {
		issuer, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse the kept issuer: %w", err)
		}
		roots.AddCert(issuer)
	}
	if len(pemBlocks(issuerPEM)) == 0 {
		return errors.New("no issuer is kept")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("the recovered certificate does not chain to the kept issuer: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return errors.New("the persisted key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse the persisted key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return errors.New("the persisted key cannot sign")
	}
	public, ok := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !public.Equal(signer.Public()) {
		return errors.New("the recovered certificate is not signed over the persisted key")
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != agent {
		return fmt.Errorf("the recovered certificate names %v, not %s", leaf.URIs, agent)
	}

	return nil
}

// pemBlocks is every CERTIFICATE block in data.
func pemBlocks(data []byte) []*pem.Block {
	var blocks []*pem.Block
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return blocks
		}
		if block.Type == pemCertificate {
			blocks = append(blocks, block)
		}
	}
}
