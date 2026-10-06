package desired

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// issuePass is how often the issuer looks for managed agents with no
	// credential. A look reads the manager's cache, so it is cheap.
	issuePass = time.Second

	// kindEpochSuperseded is the refusal of a request whose epoch is no longer
	// the agent's assignment (#218).
	kindEpochSuperseded = "epoch_superseded"
)

// Need is a managed agent whose credential is not placed: its GRN, and the
// assignment epoch its Agent's spec carries, which the feed renders.
type Need struct {
	GRN   string
	Epoch string
}

// PendingRequest is a certificate request as it is persisted before it is sent:
// the private key, the request signed over it, the epoch it asks for, and the
// request id derived from the request and the epoch.
type PendingRequest struct {
	KeyPEM []byte
	CSRPEM []byte
	Epoch  string
	ID     string
}

// CredentialStore holds what the issuer persists.
type CredentialStore interface {
	// Needed lists the managed agents whose credential is not placed. It also
	// removes the persisted request of an agent whose credential is placed,
	// which a placement interrupted before that step leaves behind.
	Needed(ctx context.Context) ([]Need, error)

	// LoadRequest returns the request persisted for agent, and false where none
	// is.
	LoadRequest(ctx context.Context, agent string) (PendingRequest, bool, error)

	// SaveRequest persists request for agent, replacing the one there.
	SaveRequest(ctx context.Context, agent string, request PendingRequest) error

	// Place writes agent's credential whole, the key beside the certificate
	// that was signed over it, and then removes the persisted request.
	Place(ctx context.Context, agent string, keyPEM []byte, certificate Certificate) error
}

// Issuer obtains the first certificate of every managed agent: an agent the
// control service created, whose certificate garam signs over a key this
// operator generates (#218). It is a manager Runnable.
//
// The key and the request are persisted before the request is sent, and the
// request id is derived from them, so a manager stopped at any point resumes
// with the same key and request and is answered the stored result. No second
// key is generated while one is persisted and unanswered.
type Issuer struct {
	client *Client
	store  CredentialStore

	// The waits, held here so a test in the package can shorten them, as the
	// puller's are.
	pass, transientFirst, transientLast, refusedWait time.Duration

	// attempts holds when each agent may be asked about next, in memory only:
	// after a restart every agent is asked once more, with the request it
	// persisted.
	attempts map[string]*attempt
}

// attempt is what the issuer remembers of an agent between passes.
type attempt struct {
	next    time.Time
	backoff time.Duration

	// superseded is the epoch a request was refused as superseded at. Until
	// the agent's epoch moves past it there is nothing new to send.
	superseded string

	// refusedAs is the last refusal logged, so a refusal is logged once and
	// not on every attempt.
	refusedAs string
}

// NewIssuer returns an Issuer asking through client and persisting through
// store.
func NewIssuer(client *Client, store CredentialStore) *Issuer {
	return &Issuer{
		client: client, store: store,
		pass: issuePass, transientFirst: transientFirst, transientLast: transientLast, refusedWait: refusedWait,
		attempts: map[string]*attempt{},
	}
}

// Start issues until ctx is cancelled. It returns no error, because an error
// from a Runnable stops the manager.
func (i *Issuer) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("desired").Info("Issuing the first certificate of managed agents")
	for ctx.Err() == nil {
		i.issueAll(ctx)
		pause(ctx, i.pass)
	}

	return nil
}

// issueAll asks for the certificate of every managed agent that needs one and
// is due.
func (i *Issuer) issueAll(ctx context.Context) {
	needs, err := i.store.Needed(ctx)
	if err != nil {
		logf.FromContext(ctx).WithName("desired").Error(err, "Failed to list the managed agents that need a certificate")

		return
	}
	for _, need := range needs {
		i.issue(ctx, need)
	}
}

// issue takes one agent one step towards its certificate.
func (i *Issuer) issue(ctx context.Context, need Need) {
	log := logf.FromContext(ctx).WithName("desired").WithValues("agent", need.GRN)
	state := i.attempts[need.GRN]
	if state == nil {
		state = &attempt{}
		i.attempts[need.GRN] = state
	}
	// A superseded epoch is asked about again as soon as the agent's epoch
	// moves; anything else waits its turn.
	epochMoved := state.superseded != "" && state.superseded != need.Epoch
	if time.Now().Before(state.next) && !epochMoved {
		return
	}
	if need.Epoch == "" {
		return
	}

	request, err := i.persistedRequest(ctx, need)
	if err != nil {
		log.Error(err, "Failed to persist the certificate request")
		i.retryTransient(state)

		return
	}

	certificate, err := i.client.RequestCertificate(ctx, need.GRN, request.ID, request.Epoch, request.CSRPEM)
	if refusal, refused := asRefusal(err); refused {
		state.backoff = 0
		state.next = time.Now().Add(i.refusedWait)
		state.superseded = ""
		if refusal.Status == http.StatusConflict && refusal.Kind == kindEpochSuperseded {
			// The key is kept: control refused before garam signed anything, so
			// no certificate exists over it. The next epoch the feed renders is
			// sent as a new request, under a new id.
			state.superseded = request.Epoch
		}
		if kind := fmt.Sprintf("%d %s", refusal.Status, refusal.Kind); kind != state.refusedAs {
			log.Error(refusal, "The control service refused the certificate request; asking again slowly",
				"status", refusal.Status, "kind", refusal.Kind, "wait", i.refusedWait)
			state.refusedAs = kind
		}

		return
	}
	if err != nil {
		log.Info("The certificate request did not answer; asking again", "error", err.Error())
		i.retryTransient(state)

		return
	}
	if certificate.Agent != need.GRN || certificate.Epoch != request.Epoch ||
		len(certificate.CertificatePEM) == 0 || len(certificate.IssuerPEM) == 0 || len(certificate.ServerRootPEM) == 0 {
		log.Error(errors.New("the answer does not match the request"), "Not placing a certificate for another agent or epoch",
			"answeredAgent", certificate.Agent, "answeredEpoch", certificate.Epoch)
		state.next = time.Now().Add(i.refusedWait)

		return
	}

	if err := i.store.Place(ctx, need.GRN, request.KeyPEM, certificate); err != nil {
		log.Error(err, "Failed to place the certificate")
		i.retryTransient(state)

		return
	}
	delete(i.attempts, need.GRN)
	log.Info("Placed the first certificate of a managed agent", "notAfter", certificate.NotAfter)
}

// persistedRequest returns the request persisted for need, persisting a new one
// first where there is none, and the agent's current epoch first where the
// persisted one asks for another. Nothing is sent that is not persisted.
func (i *Issuer) persistedRequest(ctx context.Context, need Need) (PendingRequest, error) {
	request, found, err := i.store.LoadRequest(ctx, need.GRN)
	if err != nil {
		return PendingRequest{}, err
	}
	if !found {
		request, err = newPendingRequest(need.Epoch)
		if err != nil {
			return PendingRequest{}, err
		}

		return request, i.store.SaveRequest(ctx, need.GRN, request)
	}
	if request.Epoch == need.Epoch {
		return request, nil
	}

	// Another epoch is another request: same key and CSR, a new id.
	id, err := requestID(request.CSRPEM, need.Epoch)
	if err != nil {
		return PendingRequest{}, err
	}
	request.Epoch, request.ID = need.Epoch, id

	return request, i.store.SaveRequest(ctx, need.GRN, request)
}

// retryTransient schedules the next attempt after a wait doubling to the cap.
func (i *Issuer) retryTransient(state *attempt) {
	if state.backoff == 0 {
		state.backoff = i.transientFirst
	} else {
		state.backoff = min(2*state.backoff, i.transientLast)
	}
	state.next = time.Now().Add(state.backoff)
}

// newPendingRequest generates an ECDSA P-256 key and a request over it with an
// empty subject and no SAN: garam decides what the certificate names, and reads
// only the key (#218). The key is PKCS#8 in a "PRIVATE KEY" block, as garam's
// own mint writes key.pem (garam@7ca51b9 internal/ca/service.go:110,116), so
// one credential layout carries one key encoding whichever path made it.
func newPendingRequest(epoch string) (PendingRequest, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return PendingRequest{}, fmt.Errorf("generate the agent's key: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return PendingRequest{}, fmt.Errorf("encode the agent's key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{SignatureAlgorithm: x509.ECDSAWithSHA256}, key)
	if err != nil {
		return PendingRequest{}, fmt.Errorf("sign the certificate request: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	id, err := requestID(csrPEM, epoch)
	if err != nil {
		return PendingRequest{}, err
	}

	return PendingRequest{
		KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		CSRPEM: csrPEM, Epoch: epoch, ID: id,
	}, nil
}

// requestID is hex(sha256(CSR DER || 0x00 || epoch)): one id for one key and
// one epoch, so a resumed request repeats its id and a new epoch is a new one.
func requestID(csrPEM []byte, epoch string) (string, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return "", errors.New("the persisted certificate request is not a PEM CERTIFICATE REQUEST")
	}
	sum := sha256.Sum256(bytes.Join([][]byte{block.Bytes, []byte(epoch)}, []byte{0}))

	return hex.EncodeToString(sum[:]), nil
}
