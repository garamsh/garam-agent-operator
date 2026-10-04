package definition_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

const (
	firstPin  = "sha256:aa"
	secondPin = "sha256:bb"
	webFetch  = "web_fetch"
)

// registration is one answer the registrar gives, in order. An answer with no epoch is "1".
type registration struct {
	agent definition.GRN
	epoch string
	err   error
}

// registrar answers each Register call with the next of its registrations, and counts the calls.
type registrar struct {
	mu      sync.Mutex
	answers []registration
	calls   int
}

func (r *registrar) Register(_ context.Context, _ definition.Registration) (definition.Registered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.answers) == 0 {
		return definition.Registered{}, fmt.Errorf("registrar has no answer left")
	}
	next := r.answers[0]
	r.answers = r.answers[1:]
	if next.epoch == "" {
		next.epoch = "1"
	}
	if next.err != nil {
		return definition.Registered{}, next.err
	}
	return definition.Registered{Agent: next.agent, Epoch: next.epoch}, nil
}

// fixture is a service over an in-memory repository, with one profile and one template published.
type fixture struct {
	service    definition.Service
	repository *repository.Memory
	registrar  *registrar
	issuer     *issuer
	profile    definition.ProfileRef
	template   definition.TemplateRef
}

// k8s is the controller every test's agents are created on, and the one binding() records.
const k8s = "grn:acme:default:operator:k8s"

// create is a request to create an agent on k8s from template under the fixture's profile.
func (f fixture) create(requestID string, template definition.TemplateRef) definition.CreateInput {
	return definition.CreateInput{
		Request: key(requestID),
		Binding: definition.Binding{
			Actor: actor, Operation: "agent:create", Target: k8s,
			BodySHA256: "digest-" + requestID, OperationRef: "ref-" + requestID,
		},
		Controller: k8s,
		Template:   template,
		Profile:    f.profile,
	}
}

func newFixture(t *testing.T, answers ...registration) fixture {
	t.Helper()
	ctx := context.Background()
	repo := repository.NewMemory()
	reg := &registrar{answers: answers}
	iss := &issuer{}
	svc := definition.NewService(repo, reg, iss)

	p, err := svc.PublishProfile(ctx, org, "small", settings("500m", "1Gi"))
	require.NoError(t, err)
	profile := definition.ProfileRef{Name: p.Name, Version: p.Version}

	tmpl, err := svc.PublishTemplate(ctx, org, definition.Template{
		Name:    "researcher",
		Profile: profile,
		Config:  config("first ego", definition.ToolPins{webFetch: firstPin}),
	})
	require.NoError(t, err)

	return fixture{
		service:    svc,
		repository: repo,
		registrar:  reg,
		issuer:     iss,
		profile:    profile,
		template:   definition.TemplateRef{Name: tmpl.Name, Version: tmpl.Version},
	}
}

func settings(cpu, storage string) definition.ExecutionSettings {
	class := "standard"
	return definition.ExecutionSettings{
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)},
		},
		StorageSize:      resource.MustParse(storage),
		StorageClassName: &class,
	}
}

func config(ego string, tools definition.ToolPins) definition.Configuration {
	return definition.Configuration{
		Model: definition.Model{
			Provider: "anthropic",
			BaseURL:  "https://api.anthropic.com",
			Name:     "claude-opus-5-5",
			APIKey:   "model-api-key/api-key",
		},
		Ego:   ego,
		Tools: tools,
	}
}

const (
	// actor is the user every test's requests are made for.
	actor = "grn:acme:default:user:7c1d"
	// org is the organization every test's requests are made in, unless the test names another.
	org = "acme"
)

// other is a value no test's request binds, standing in for one changed on a repeat.
const other = "other"

func key(requestID string) definition.RequestKey {
	return definition.RequestKey{Organization: org, RequestID: requestID}
}
