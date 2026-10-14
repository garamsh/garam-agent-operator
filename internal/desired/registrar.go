package desired

import (
	"context"
	"fmt"
	"net/http"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// registerPass is how often the registrar looks at the managed agents'
// placements. A look reads the manager's cache, so it is cheap.
const registerPass = time.Second

// The 409 kinds that refuse a placement for good: the Pod it names has been
// replaced, or the epoch is no longer the agent's. The same body is never sent
// again; a new Pod, epoch or token is a new body (#218).
const (
	kindPlacementSuperseded = "placement_superseded"
)

// CurrentPlacement is a managed agent's current placement, under its GRN.
type CurrentPlacement struct {
	GRN       string
	Placement Placement
}

// PlacementStore reads the managed agents' current placements.
type PlacementStore interface {
	// Current lists the current placement of every agent that registers one:
	// an agent on the control source, with garam's adapter placed, whose Pod
	// runs and has recorded the claim it started on.
	Current(ctx context.Context) ([]CurrentPlacement, error)
}

// Registrar registers each managed agent's placement with the control service
// once it is accepted (#212, #218). It is a manager Runnable.
type Registrar struct {
	client *Client
	store  PlacementStore

	// The waits, held here so a test in the package can shorten them.
	pass, transientFirst, transientLast, refusedWait time.Duration

	// registrations holds what the registrar knows of each agent's current
	// placement, in memory only: after a restart every placement is presented
	// once more, which the control service answers as a repeat.
	registrations map[string]*registration
}

// registration is what the registrar remembers of one agent's placement. It
// starts afresh whenever the placement changes, so what it remembers is bounded
// by one placement per agent.
type registration struct {
	placement Placement

	// accepted is set once the control service accepted this placement.
	accepted bool

	// superseded is set when this placement was refused for good; it is never
	// sent again.
	superseded bool

	next      time.Time
	backoff   time.Duration
	refusedAs string
}

// NewRegistrar returns a Registrar presenting placements through client, read
// from store.
func NewRegistrar(client *Client, store PlacementStore) *Registrar {
	return &Registrar{
		client: client, store: store,
		pass: registerPass, transientFirst: transientFirst, transientLast: transientLast, refusedWait: refusedWait,
		registrations: map[string]*registration{},
	}
}

// Start registers until ctx is cancelled. It returns no error, because an
// error from a Runnable stops the manager.
func (r *Registrar) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("desired").Info("Registering managed agents' placements")
	for ctx.Err() == nil {
		r.registerAll(ctx)
		pause(ctx, r.pass)
	}

	return nil
}

// registerAll presents every current placement that the control service has
// not accepted.
func (r *Registrar) registerAll(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("desired")
	current, err := r.store.Current(ctx)
	if err != nil {
		log.Error(err, "Failed to list the managed agents' placements")

		return
	}

	listed := make(map[string]bool, len(current))
	for _, c := range current {
		listed[c.GRN] = true
		r.register(ctx, c)
	}
	// An agent with no current placement — its Pod gone, or its Agent
	// deleted — keeps nothing here.
	for grn := range r.registrations {
		if !listed[grn] {
			delete(r.registrations, grn)
		}
	}
}

// register presents one placement, where it is due.
func (r *Registrar) register(ctx context.Context, current CurrentPlacement) {
	log := logf.FromContext(ctx).WithName("desired").WithValues("agent", current.GRN, "podUID", current.Placement.PodUID)
	state := r.registrations[current.GRN]
	if state == nil || !samePlacement(state.placement, current.Placement) {
		state = &registration{placement: current.Placement}
		r.registrations[current.GRN] = state
	}
	if state.superseded || state.accepted || time.Now().Before(state.next) {
		return
	}

	created, err := r.client.RegisterPlacement(ctx, current.GRN, current.Placement)
	if refusal, refused := asRefusal(err); refused {
		state.backoff = 0
		kind := fmt.Sprintf("%d %s", refusal.Status, refusal.Kind)
		if refusal.Status == http.StatusConflict &&
			(refusal.Kind == kindPlacementSuperseded || refusal.Kind == kindEpochSuperseded) {
			state.superseded = true
			log.Error(refusal, "The control service refused this placement for good; it is not presented again",
				"status", refusal.Status, "kind", refusal.Kind)

			return
		}
		state.next = time.Now().Add(r.refusedWait)
		if kind != state.refusedAs {
			log.Error(refusal, "The control service refused the placement; presenting it again slowly",
				"status", refusal.Status, "kind", refusal.Kind, "wait", r.refusedWait)
			state.refusedAs = kind
		}

		return
	}
	if err != nil {
		log.Info("The placement registration did not answer; presenting it again", "error", err.Error())
		if state.backoff == 0 {
			state.backoff = r.transientFirst
		} else {
			state.backoff = min(2*state.backoff, r.transientLast)
		}
		state.next = time.Now().Add(state.backoff)

		return
	}

	state.accepted, state.backoff, state.refusedAs = true, 0, ""
	if created {
		log.Info("Registered the agent's placement")
	} else {
		log.Info("The agent's placement was already registered")
	}
}

// samePlacement reports whether two placements are the same body.
func samePlacement(a, b Placement) bool {
	if a.Epoch != b.Epoch || a.PodUID != b.PodUID || a.PVCUID != b.PVCUID || a.TokenSHA256 != b.TokenSHA256 {
		return false
	}
	if a.Previous == nil || b.Previous == nil {
		return a.Previous == b.Previous
	}

	return *a.Previous == *b.Previous
}
