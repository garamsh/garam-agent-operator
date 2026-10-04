package repository

import (
	"context"
	"maps"
	"sync"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// Memory is a definition.Repository held in process, for tests and development.
type Memory struct {
	mu          sync.Mutex
	profiles    map[string][]definition.Profile
	templates   map[string][]definition.Template
	definitions map[definition.GRN][]definition.Definition
	creations   map[definition.RequestKey]definition.Creation
	requests    map[definition.RequestKey]definition.Request
}

var _ definition.Repository = (*Memory)(nil)

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{
		profiles:    map[string][]definition.Profile{},
		templates:   map[string][]definition.Template{},
		definitions: map[definition.GRN][]definition.Definition{},
		creations:   map[definition.RequestKey]definition.Creation{},
		requests:    map[definition.RequestKey]definition.Request{},
	}
}

func (m *Memory) PublishProfile(_ context.Context, name string, settings definition.ExecutionSettings) (definition.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := definition.Profile{
		Name:     name,
		Version:  definition.Version(len(m.profiles[name]) + 1),
		Settings: cloneSettings(settings),
	}
	m.profiles[name] = append(m.profiles[name], p)
	return cloneProfile(p), nil
}

func (m *Memory) GetProfile(_ context.Context, ref definition.ProfileRef) (definition.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.profiles[ref.Name]
	if ref.Version < 1 || int(ref.Version) > len(versions) {
		return definition.Profile{}, definition.ErrNotFound
	}
	return cloneProfile(versions[ref.Version-1]), nil
}

func (m *Memory) PublishTemplate(_ context.Context, t definition.Template) (definition.Template, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t = cloneTemplate(t)
	t.Version = definition.Version(len(m.templates[t.Name]) + 1)
	m.templates[t.Name] = append(m.templates[t.Name], t)
	return cloneTemplate(t), nil
}

func (m *Memory) GetTemplate(_ context.Context, ref definition.TemplateRef) (definition.Template, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.templates[ref.Name]
	if ref.Version < 1 || int(ref.Version) > len(versions) {
		return definition.Template{}, definition.ErrNotFound
	}
	return cloneTemplate(versions[ref.Version-1]), nil
}

func (m *Memory) Configure(_ context.Context, r definition.Request, d definition.Definition) (definition.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.requests[r.Key]; ok {
		return stored, nil
	}
	if len(m.definitions[d.Agent]) == 0 {
		return definition.Request{}, definition.ErrNotFound
	}
	r.Outcome = definition.Stale{}
	if err := m.appendLocked(d); err == nil {
		r.Outcome = definition.Applied{Revision: d.Revision}
	}
	m.requests[r.Key] = r
	return r, nil
}

func (m *Memory) GetDefinition(_ context.Context, agent definition.GRN) (definition.Definition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	revisions := m.definitions[agent]
	if len(revisions) == 0 {
		return definition.Definition{}, definition.ErrNotFound
	}
	return cloneDefinition(revisions[len(revisions)-1]), nil
}

func (m *Memory) BeginCreation(_ context.Context, c definition.Creation) (definition.Creation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.creations[c.Key]; ok {
		return stored, nil
	}
	c.Outcome = definition.Pending{}
	m.creations[c.Key] = c
	return c, nil
}

func (m *Memory) RegisterCreation(_ context.Context, key definition.RequestKey, d definition.Definition) (definition.Creation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creations[key]
	if !ok {
		return definition.Creation{}, definition.ErrNotFound
	}
	if _, pending := c.Outcome.(definition.Pending); !pending {
		return c, nil
	}
	if err := m.appendLocked(d); err != nil {
		return definition.Creation{}, err
	}
	c.Outcome = definition.Registered{Agent: d.Agent}
	m.creations[key] = c
	return c, nil
}

func (m *Memory) FailCreation(_ context.Context, key definition.RequestKey, reason string) (definition.Creation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creations[key]
	if !ok {
		return definition.Creation{}, definition.ErrNotFound
	}
	if _, pending := c.Outcome.(definition.Pending); !pending {
		return c, nil
	}
	c.Outcome = definition.Failed{Reason: reason}
	m.creations[key] = c
	return c, nil
}

// appendLocked stores d when its revision is one past the agent's latest. m.mu is held.
func (m *Memory) appendLocked(d definition.Definition) error {
	revisions := m.definitions[d.Agent]
	if int(d.Revision) != len(revisions)+1 {
		return definition.ErrStaleRevision
	}
	m.definitions[d.Agent] = append(revisions, cloneDefinition(d))
	return nil
}

func cloneSettings(s definition.ExecutionSettings) definition.ExecutionSettings {
	c := definition.ExecutionSettings{
		Resources:   *s.Resources.DeepCopy(),
		StorageSize: s.StorageSize.DeepCopy(),
	}
	if s.StorageClassName != nil {
		name := *s.StorageClassName
		c.StorageClassName = &name
	}
	return c
}

func cloneConfig(c definition.Configuration) definition.Configuration {
	c.Tools = maps.Clone(c.Tools)
	return c
}

func cloneProfile(p definition.Profile) definition.Profile {
	p.Settings = cloneSettings(p.Settings)
	return p
}

func cloneTemplate(t definition.Template) definition.Template {
	t.Config = cloneConfig(t.Config)
	return t
}

func cloneDefinition(d definition.Definition) definition.Definition {
	d.Config = cloneConfig(d.Config)
	return d
}
