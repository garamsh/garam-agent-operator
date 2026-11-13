// Package agent reconciles the managed agents the control service's
// feed names into gaon objects: one Secret, two Volumes and one MicroVM
// each (ADR-0151 §2, §3 and §5).
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

// Desired is one agent of the control service's feed: the latest
// revision of an agent that garam proves placed on this operator. The
// wire form is parsed where a Feed is implemented, so the rest of the
// package holds only values it can trust.
type Desired struct {
	GRN grn.GRN
	// Epoch is the epoch the operator holds the agent at, and carries on
	// every report.
	Epoch int64
	// Revision is the revision the feed releases, a canonical decimal
	// string as contract C2 writes it.
	Revision string
	// Stopped is the feed's "stopped": true.
	Stopped bool
	// MemoryStorage and WorkspaceStorage size the agent's two Volumes.
	MemoryStorage    api.Quantity
	WorkspaceStorage api.Quantity
	// Configuration is what the runtime is configured with. It is read
	// as the wire gave it and checked where it is rendered, per agent.
	Configuration Configuration
}

// State is a provisioning state the operator reports. `pending` is
// never sent, because it is what garam holds before any report.
type State string

// The states the operator reports (platform/api/machine.yaml
// ProvisioningState).
const (
	StateProvisioned State = "provisioned"
	StateReady       State = "ready"
	StateFailed      State = "failed"
)

// ErrStaleEpoch is what Garam.ReportState returns when garam refused
// the report for an epoch it no longer holds the agent at
// (failed_precondition).
var ErrStaleEpoch = errorx.New(errorx.KindFailedPrecondition, "agent epoch is stale")

// The labels and annotations of every object the operator writes
// (ADR-0151 §2 and §4).
const (
	labelRole         = "garam.sh/role"
	labelOrganization = "garam.sh/organization"
	labelAgent        = "garam.sh/agent"
	annotationGRN     = "garam.sh/agent-grn"
	annotationEpoch   = "garam.sh/epoch"
	annotationRev     = "garam.sh/revision"
	roleAgent         = "agent"
	// roleModelKey labels a Secret that holds a model or embedding key
	// (ADR-0154). The deployment creates it; the operator never does.
	roleModelKey = "model-key"
)

// Selector selects the objects this operator owns, for a client's
// api.WithLabelSelector.
func Selector() api.Selector {
	return api.Selector{{Key: labelRole, Operator: api.SelectorIn, Values: []string{roleAgent}}}
}

// ModelKeySelector selects the Secrets that may hold a model key, for a
// client's api.WithLabelSelector. It is not Selector: the role the
// operator's gaon identity needs is read on Secrets labelled
// `garam.sh/role=model-key` as well as on its own.
func ModelKeySelector() api.Selector {
	return api.Selector{{Key: labelRole, Operator: api.SelectorIn, Values: []string{roleModelKey}}}
}

// ModelKeyLabels are the labels a model-key Secret of g's organization
// carries, which is how the deployment creates it.
func ModelKeyLabels(g grn.GRN) map[string]string {
	return map[string]string{labelRole: roleModelKey, labelOrganization: organizationLabel(g)}
}

// organizationLabel is the value of the organization label of g's
// objects: the first 32 hex digits of the SHA-256 of the organization's
// GRN, because a label value is at most 63 characters.
func organizationLabel(g grn.GRN) string {
	return digest(grn.Org(g.Organization).String())[:32]
}

// names are the names of the objects of one agent.
type names struct {
	secret, microvm, memory, workspace string
}

// digest is the hex SHA-256 of s.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// namesOf returns the names of g's objects: `agent-` and sixteen hex
// characters of the SHA-256 of the GRN, which gaon's flat namespace
// needs because a GRN holds characters a name does not.
func namesOf(g grn.GRN) names {
	base := "agent-" + digest(g.String())[:16]
	return names{secret: base, microvm: base, memory: base + "-memory", workspace: base + "-workspace"}
}

// metaFor returns the metadata every object of d carries. A label value
// is at most 63 characters, so the two digests are cut to 32.
func metaFor(name string, d Desired) api.ObjectMeta {
	return api.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			labelRole:         roleAgent,
			labelOrganization: organizationLabel(d.GRN),
			labelAgent:        digest(d.GRN.String())[:32],
		},
		Annotations: map[string]string{
			annotationGRN:   d.GRN.String(),
			annotationEpoch: strconv.FormatInt(d.Epoch, 10),
			annotationRev:   d.Revision,
		},
	}
}

// Credentials are the files of an agent's credential, by the name each
// has as a key of its Secret. It is never logged: a log of it prints
// its names and not its values.
type Credentials map[string][]byte

// LogValue keeps the values out of a log line.
func (c Credentials) LogValue() slog.Value {
	return slog.StringValue("credentials redacted")
}

// String keeps the values out of a formatted message.
func (c Credentials) String() string { return "credentials redacted" }

// GoString keeps the values out of a %#v.
func (c Credentials) GoString() string { return "credentials redacted" }
