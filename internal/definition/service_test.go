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
)

// registration is one answer the registrar gives, in order.
type registration struct {
	agent definition.GRN
	err   error
}

// registrar answers each Register call with the next of its registrations.
type registrar struct {
	mu      sync.Mutex
	answers []registration
}

func (r *registrar) Register(_ context.Context, _ definition.CreationKey) (definition.GRN, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.answers) == 0 {
		return "", fmt.Errorf("registrar has no answer left")
	}
	next := r.answers[0]
	r.answers = r.answers[1:]
	return next.agent, next.err
}

// fixture is a service over an in-memory repository, with one profile and one template published.
type fixture struct {
	service    definition.Service
	repository *repository.Memory
	profile    definition.ProfileRef
	template   definition.TemplateRef
}

func newFixture(t *testing.T, answers ...registration) fixture {
	t.Helper()
	ctx := context.Background()
	repo := repository.NewMemory()
	svc := definition.NewService(repo, &registrar{answers: answers})

	p, err := svc.PublishProfile(ctx, "small", settings("500m", "1Gi"))
	require.NoError(t, err)
	profile := definition.ProfileRef{Name: p.Name, Version: p.Version}

	tmpl, err := svc.PublishTemplate(ctx, definition.Template{
		Name:    "researcher",
		Profile: profile,
		Config:  config("first ego", definition.ToolPins{"web_fetch": firstPin}),
	})
	require.NoError(t, err)

	return fixture{
		service:    svc,
		repository: repo,
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
			APIKey:   "model-api-key",
		},
		Ego:   ego,
		Tools: tools,
	}
}

func key(requestID string) definition.CreationKey {
	return definition.CreationKey{
		Actor:        "grn:acme:default:user:7c1d",
		Organization: "acme",
		RequestID:    requestID,
	}
}
