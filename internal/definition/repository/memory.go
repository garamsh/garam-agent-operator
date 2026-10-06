package repository

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// Memory is a definition.Repository held in process, for tests and development.
type Memory struct {
	mu          sync.Mutex
	profiles    map[named][]definition.Profile
	templates   map[named][]definition.Template
	definitions map[definition.GRN][]definition.Definition
	// positions holds, beside each agent's revisions, the position each was stored at.
	positions    map[definition.GRN][]definition.Position
	position     definition.Position
	statuses     map[definition.GRN]definition.Status
	creations    map[definition.RequestKey]definition.Creation
	requests     map[definition.RequestKey]definition.Request
	certificates map[definition.GRN]definition.InitialCertificate
	// placements holds every placement stored for an agent, by Pod UID, superseded ones too.
	placements map[definition.GRN]map[string]definition.Placement
}

var _ definition.Repository = (*Memory)(nil)

// named is a profile's or a template's name within the organization that published it.
type named struct {
	org  string
	name string
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{
		profiles:    map[named][]definition.Profile{},
		templates:   map[named][]definition.Template{},
		definitions: map[definition.GRN][]definition.Definition{},
		positions:   map[definition.GRN][]definition.Position{},
		statuses:    map[definition.GRN]definition.Status{},
		creations:   map[definition.RequestKey]definition.Creation{},
		requests:    map[definition.RequestKey]definition.Request{},

		certificates: map[definition.GRN]definition.InitialCertificate{},
		placements:   map[definition.GRN]map[string]definition.Placement{},
	}
}

func (m *Memory) PublishProfile(_ context.Context, org, name string, settings definition.ExecutionSettings) (definition.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := named{org: org, name: name}
	p := definition.Profile{
		Name:     name,
		Version:  definition.Version(len(m.profiles[key]) + 1),
		Settings: cloneSettings(settings),
	}
	m.profiles[key] = append(m.profiles[key], p)
	return cloneProfile(p), nil
}

func (m *Memory) GetProfile(_ context.Context, org string, ref definition.ProfileRef) (definition.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.profiles[named{org: org, name: ref.Name}]
	if ref.Version < 1 || int(ref.Version) > len(versions) {
		return definition.Profile{}, definition.ErrNotFound
	}
	return cloneProfile(versions[ref.Version-1]), nil
}

func (m *Memory) PublishTemplate(_ context.Context, org string, t definition.Template) (definition.Template, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := named{org: org, name: t.Name}
	t = cloneTemplate(t)
	t.Version = definition.Version(len(m.templates[key]) + 1)
	m.templates[key] = append(m.templates[key], t)
	return cloneTemplate(t), nil
}

func (m *Memory) GetTemplate(_ context.Context, org string, ref definition.TemplateRef) (definition.Template, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.templates[named{org: org, name: ref.Name}]
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
	if revisions := m.definitions[d.Agent]; len(revisions) == 0 || revisions[0].Organization != d.Organization {
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

func (m *Memory) Position(_ context.Context) (definition.Position, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.position, nil
}

func (m *Memory) Desired(_ context.Context, operator string, limit int) (definition.DesiredPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type owed struct {
		revision definition.DesiredRevision
		position definition.Position
	}
	var found []owed
	for agent, revisions := range m.definitions {
		latest := revisions[len(revisions)-1]
		if latest.Assignment == nil || latest.Assignment.Operator != operator {
			continue
		}
		profile := m.profiles[named{org: latest.Organization, name: latest.Profile.Name}][latest.Profile.Version-1]
		found = append(found, owed{
			revision: definition.DesiredRevision{Definition: cloneDefinition(latest), Settings: cloneSettings(profile.Settings)},
			position: m.positions[agent][len(revisions)-1],
		})
	}
	slices.SortFunc(found, func(a, b owed) int { return cmp.Compare(a.position, b.position) })
	page := definition.DesiredPage{Position: m.position}
	for _, o := range found[:min(limit, len(found))] {
		page.Revisions = append(page.Revisions, o.revision)
	}
	return page, nil
}

func (m *Memory) RecordStatus(_ context.Context, agent definition.GRN, s definition.Status) (definition.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.statuses[agent]
	stored.Observed = max(stored.Observed, s.Observed)
	stored.Rendered = max(stored.Rendered, s.Rendered)
	m.statuses[agent] = stored
	return stored, nil
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

func (m *Memory) RegisterCreation(
	_ context.Context, key definition.RequestKey, d definition.Definition,
) (definition.Creation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creations[key]
	if !ok {
		return definition.Creation{}, false, definition.ErrNotFound
	}
	if _, pending := c.Outcome.(definition.Pending); !pending {
		return c, false, nil
	}
	if err := m.appendLocked(d); err != nil {
		return definition.Creation{}, false, err
	}
	c.Outcome = definition.Registered{Agent: d.Agent, Epoch: d.Assignment.Epoch}
	m.creations[key] = c
	return c, true, nil
}

func (m *Memory) FailCreation(_ context.Context, key definition.RequestKey, failed definition.Failed) (definition.Creation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creations[key]
	if !ok {
		return definition.Creation{}, definition.ErrNotFound
	}
	if _, pending := c.Outcome.(definition.Pending); !pending {
		return c, nil
	}
	c.Outcome = failed
	m.creations[key] = c
	return c, nil
}

// appendLocked stores d when its revision is one past the agent's latest. m.mu is held.
func (m *Memory) CreationOf(_ context.Context, agent definition.GRN) (definition.Creation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.creations {
		if r, ok := c.Outcome.(definition.Registered); ok && r.Agent == agent {
			return c, nil
		}
	}
	return definition.Creation{}, definition.ErrNotFound
}

func (m *Memory) BeginInitialCertificate(
	_ context.Context, agent definition.GRN, r definition.CertificateRequest,
) (definition.InitialCertificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.certificates[agent]
	if !ok {
		c = definition.InitialCertificate{Agent: agent, Request: r}
		m.certificates[agent] = c
	}
	return cloneCertificate(c), nil
}

func (m *Memory) IssueInitialCertificate(
	_ context.Context, agent definition.GRN, r definition.CertificateRequest, issued definition.IssuedCertificate,
) (definition.InitialCertificate, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.certificates[agent]
	if !ok {
		return definition.InitialCertificate{}, false, definition.ErrNotFound
	}
	if c.Issued != nil || c.Request != r {
		return cloneCertificate(c), false, nil
	}
	c.Issued = &issued
	m.certificates[agent] = c
	return cloneCertificate(c), true, nil
}

func (m *Memory) ClearInitialCertificate(_ context.Context, agent definition.GRN, r definition.CertificateRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.certificates[agent]; ok && c.Issued == nil && c.Request == r {
		delete(m.certificates, agent)
	}
	return nil
}

func (m *Memory) RegisterPlacement(_ context.Context, in definition.PlacementInput) (definition.Placement, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.placements[in.Agent]
	var current, same *definition.Placement
	for _, p := range stored {
		if !p.Superseded {
			current = &p
		}
	}
	if p, ok := stored[in.Request.PodUID]; ok {
		same = &p
	}
	action, err := definition.DecidePlacement(current, same, in)
	if err != nil {
		return definition.Placement{}, false, err
	}
	switch action {
	case definition.PlacementRepeat:
		return clonePlacement(*same), false, nil
	case definition.PlacementRefresh:
		same.LeafDER = slices.Clone(in.LeafDER)
		stored[same.Request.PodUID] = *same
		return clonePlacement(*same), false, nil
	}
	if stored == nil {
		stored = map[string]definition.Placement{}
		m.placements[in.Agent] = stored
	}
	if current != nil {
		current.Superseded = true
		stored[current.Request.PodUID] = *current
	}
	p := definition.Placement{Agent: in.Agent, Controller: in.Controller, Request: in.Request, LeafDER: slices.Clone(in.LeafDER)}
	stored[p.Request.PodUID] = p
	return clonePlacement(p), true, nil
}

func (m *Memory) appendLocked(d definition.Definition) error {
	revisions := m.definitions[d.Agent]
	if int(d.Revision) != len(revisions)+1 {
		return definition.ErrStaleRevision
	}
	m.position++
	m.definitions[d.Agent] = append(revisions, cloneDefinition(d))
	m.positions[d.Agent] = append(m.positions[d.Agent], m.position)
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
	if d.Assignment != nil {
		a := *d.Assignment
		d.Assignment = &a
	}
	return d
}

func cloneCertificate(c definition.InitialCertificate) definition.InitialCertificate {
	if c.Issued != nil {
		issued := *c.Issued
		c.Issued = &issued
	}
	return c
}

func clonePlacement(p definition.Placement) definition.Placement {
	p.LeafDER = slices.Clone(p.LeafDER)
	return p
}
