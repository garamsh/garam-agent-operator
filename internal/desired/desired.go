package desired

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Answer is one answer of the desired feed: the controller's whole releasable
// set, and the position to ask after next (ADR 0040).
type Answer struct {
	Cursor string
	Agents []Agent
}

// Agent is one agent's latest released revision.
type Agent struct {
	// GRN is the agent's garam resource name.
	GRN string

	// Revision is the revision, as the canonical decimal string the wire
	// carries.
	Revision string

	// Epoch is the assignment epoch the revision was recorded for, opaque.
	Epoch string

	Profile       Profile
	Configuration Configuration

	// Origin is OriginCutover for an agent whose revisions began with a
	// switched cutover import, and empty for every other (ADR 0050, #217). It is
	// carried as the wire has it: a value outside that set is the renderer's to
	// refuse.
	Origin string
}

// OriginCutover is the origin of an agent garam recorded as switched from the
// source it was built from: the manager takes such an agent over (#217).
const OriginCutover = "cutover"

// Profile is the execution settings of the profile version a revision names.
type Profile struct {
	Name             string
	Version          int64
	Resources        corev1.ResourceRequirements
	StorageSize      string
	StorageClassName *string
}

// Configuration is the configuration a revision delivers to the agent.
type Configuration struct {
	Model Model
	Ego   string
	Tools map[string]string
}

// Model is the model a revision configures. APIKeyRef names the Secret key
// holding its key as "<secret-name>/<key>" in the Agent's namespace (ADR 0043).
type Model struct {
	Provider  string
	BaseURL   string
	Name      string
	APIKeyRef string

	// Embedding is the embeddings endpoint the agent's memory is recalled with,
	// nil where the revision names none (ADR 0052).
	Embedding *Embedding
}

// Embedding is an embeddings endpoint. APIKeyRef is "<secret-name>/<key>" as
// the model's is, and empty where the endpoint takes no key.
type Embedding struct {
	BaseURL   string
	Name      string
	APIKeyRef string
}

// Certificate is a managed agent's first certificate as the control service
// answers it: the certificate, the authority that signed it, and the root garam's
// listener is verified against. Its private key never leaves this operator.
type Certificate struct {
	Agent          string
	Epoch          string
	CertificatePEM []byte
	IssuerPEM      []byte
	ServerRootPEM  []byte
	NotAfter       time.Time
}

// Placement is one placement of a managed agent as the control service
// registers it: the Pod running it, the state claim it started on, the
// assignment epoch it runs at, the digest of the token minted for it, and the
// placement it replaces, nil for the first.
type Placement struct {
	Epoch       string
	PodUID      string
	PVCUID      string
	TokenSHA256 string
	Previous    *PreviousPlacement
}

// PreviousPlacement is the placement a new one replaces: its Pod, and the
// digest of the writer-stopped evidence its release recorded.
type PreviousPlacement struct {
	PodUID              string
	WriterStoppedSHA256 string
}

// Renderer writes an agent's desired state into the Agent this operator builds
// for it.
type Renderer interface {
	// Render creates the Agent for agent where none exists, with Control as
	// its source, and otherwise writes the revision's fields into it. It returns
	// ErrNotControlSource for an Agent whose desired state comes from elsewhere,
	// and ErrMalformed for a revision it cannot render; both leave the Agent as
	// it was.
	Render(ctx context.Context, agent Agent) error
}

// ErrNotControlSource is a revision for an agent whose desired state another
// source holds. One source holds a GRN at a time, so it is not rendered.
var ErrNotControlSource = errors.New("the agent's desired state does not come from the control service")

// ErrMalformed is a revision that cannot be rendered as it stands.
var ErrMalformed = errors.New("the revision cannot be rendered")

// RefusalError is a 4xx answer of the control service: a definite answer, which
// asking again unchanged does not change.
type RefusalError struct {
	// Route is the route that answered, "desired" or "status".
	Route string

	// Status is the HTTP status.
	Status int

	// Kind is the kind the answer named, empty where it named none.
	Kind string

	// Message is the answer's message.
	Message string
}

func (e *RefusalError) Error() string {
	if e.Kind != "" {
		return fmt.Sprintf("control refused the %s route with %d %s: %s", e.Route, e.Status, e.Kind, e.Message)
	}

	return fmt.Sprintf("control refused the %s route with %d: %s", e.Route, e.Status, e.Message)
}
