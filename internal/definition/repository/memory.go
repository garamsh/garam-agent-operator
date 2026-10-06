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
	// locks serializes each agent's activation attempts; activations holds every activation
	// request by its request id, latest each agent's most recent activation, and applied what its
	// runtime last reported effective.
	locks       map[definition.GRN]*sync.Mutex
	activations map[definition.GRN]map[string]definition.Activation
	latest      map[definition.GRN]string
	applied     map[definition.GRN]definition.RuntimeApplied
	// cutovers holds each agent's cutover import.
	cutovers map[definition.GRN]definition.CutoverImport
	// publications holds every publish request, by its key.
	publications map[definition.RequestKey]definition.Publication
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
		locks:        map[definition.GRN]*sync.Mutex{},
		activations:  map[definition.GRN]map[string]definition.Activation{},
		latest:       map[definition.GRN]string{},
		applied:      map[definition.GRN]definition.RuntimeApplied{},
		cutovers:     map[definition.GRN]definition.CutoverImport{},
		publications: map[definition.RequestKey]definition.Publication{},
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

func (m *Memory) ListTemplates(_ context.Context, org string) ([]definition.Template, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest []definition.Template
	for key, versions := range m.templates {
		if key.org == org && len(versions) > 0 {
			latest = append(latest, cloneTemplate(versions[len(versions)-1]))
		}
	}
	slices.SortFunc(latest, func(a, b definition.Template) int { return cmp.Compare(a.Name, b.Name) })
	return latest, nil
}

func (m *Memory) ListProfiles(_ context.Context, org string) ([]definition.ProfileRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var refs []definition.ProfileRef
	for key, versions := range m.profiles {
		if key.org != org {
			continue
		}
		for _, p := range versions {
			refs = append(refs, definition.ProfileRef{Name: p.Name, Version: p.Version})
		}
	}
	slices.SortFunc(refs, func(a, b definition.ProfileRef) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Version, b.Version))
	})
	return refs, nil
}

func (m *Memory) PublishOnce(_ context.Context, p definition.Publication, t definition.Template) (definition.Publication, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.publications[p.Key]; ok {
		return stored, false, nil
	}
	key := named{org: p.Key.Organization, name: t.Name}
	t = cloneTemplate(t)
	t.Version = definition.Version(len(m.templates[key]) + 1)
	m.templates[key] = append(m.templates[key], t)
	p.Template = definition.TemplateRef{Name: t.Name, Version: t.Version}
	m.publications[p.Key] = p
	return p, true, nil
}

func (m *Memory) GetStatus(_ context.Context, agent definition.GRN) (definition.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statuses[agent], nil
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
			revision: definition.DesiredRevision{
				Definition: cloneDefinition(latest), Settings: cloneSettings(profile.Settings),
				Cutover: m.cutovers[agent].Stage == definition.CutoverSwitched,
			},
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

func (m *Memory) CurrentPlacement(_ context.Context, agent definition.GRN) (definition.Placement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.placements[agent] {
		if !p.Superseded {
			return clonePlacement(p), nil
		}
	}
	return definition.Placement{}, definition.ErrNotFound
}

func (m *Memory) WithAgentLock(ctx context.Context, agent definition.GRN, fn func(context.Context) error) error {
	m.mu.Lock()
	lock, ok := m.locks[agent]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[agent] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return fn(ctx)
}

func (m *Memory) InsertActivation(_ context.Context, a definition.Activation) (definition.Activation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.activations[a.Agent]
	if stored == nil {
		stored = map[string]definition.Activation{}
		m.activations[a.Agent] = stored
	}
	if existing, ok := stored[a.Request.RequestID]; ok {
		return existing, nil
	}
	stored[a.Request.RequestID] = a
	return a, nil
}

func (m *Memory) GetActivation(_ context.Context, agent definition.GRN, requestID string) (definition.Activation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.activations[agent][requestID]
	if !ok {
		return definition.Activation{}, definition.ErrNotFound
	}
	return a, nil
}

func (m *Memory) RecordActivation(_ context.Context, agent definition.GRN, requestID, activationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.activations[agent][requestID]
	if !ok {
		return definition.ErrNotFound
	}
	if a.ActivationID != "" && a.ActivationID != activationID {
		return definition.ErrActivationMismatch
	}
	a.ActivationID = activationID
	m.activations[agent][requestID] = a
	m.latest[agent] = activationID
	return nil
}

func (m *Memory) LatestActivation(_ context.Context, agent definition.GRN) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latest, ok := m.latest[agent]
	if !ok {
		return "", definition.ErrNotFound
	}
	return latest, nil
}

func (m *Memory) ActivationOfGeneration(_ context.Context, agent definition.GRN, generation string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.activations[agent] {
		if a.Request.Generation == generation && a.ActivationID != "" {
			return a.ActivationID, nil
		}
	}
	return "", definition.ErrNotFound
}

func (m *Memory) ConfigureReference(_ context.Context, agent definition.GRN, revision definition.Revision) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.requests {
		if applied, ok := r.Outcome.(definition.Applied); ok && r.Agent == agent && applied.Revision == revision {
			return r.Binding.OperationRef, nil
		}
	}
	return "", definition.ErrNotFound
}

func (m *Memory) RecordRuntimeApplied(_ context.Context, agent definition.GRN, applied definition.RuntimeApplied) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applied[agent] = applied
	status := m.statuses[agent]
	status.Applied = &applied.Revision
	m.statuses[agent] = status
	return nil
}

func (m *Memory) GetRuntimeApplied(_ context.Context, agent definition.GRN) (definition.RuntimeApplied, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	applied, ok := m.applied[agent]
	if !ok {
		return definition.RuntimeApplied{}, definition.ErrNotFound
	}
	return applied, nil
}

func (m *Memory) BeginCutoverImport(
	_ context.Context, imp definition.CutoverImport, d definition.Definition,
) (definition.CutoverImport, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.cutovers[imp.Agent]; ok {
		return cloneImport(stored), false, nil
	}
	if len(m.definitions[imp.Agent]) > 0 {
		return definition.CutoverImport{}, false, definition.ErrAlreadyDefined
	}
	if err := m.appendLocked(d); err != nil {
		return definition.CutoverImport{}, false, err
	}
	m.cutovers[imp.Agent] = cloneImport(imp)
	return cloneImport(imp), true, nil
}

func (m *Memory) GetCutoverImport(_ context.Context, agent definition.GRN) (definition.CutoverImport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	imp, ok := m.cutovers[agent]
	if !ok {
		return definition.CutoverImport{}, definition.ErrNotFound
	}
	return cloneImport(imp), nil
}

// cutoverLocked is the agent's import under importID, or the refusal for its absence or another.
func (m *Memory) cutoverLocked(agent definition.GRN, importID string) (definition.CutoverImport, error) {
	imp, ok := m.cutovers[agent]
	if !ok {
		return definition.CutoverImport{}, definition.ErrNotFound
	}
	if imp.ImportID != importID {
		return definition.CutoverImport{}, definition.ErrImportOpen
	}
	return imp, nil
}

func (m *Memory) FreezeCutoverImport(_ context.Context, agent definition.GRN, importID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	imp, err := m.cutoverLocked(agent, importID)
	if err != nil {
		return err
	}
	if imp.Stage != definition.CutoverImported && imp.Stage != definition.CutoverFrozen {
		return definition.ErrCutoverStage
	}
	imp.Stage = definition.CutoverFrozen
	m.cutovers[agent] = imp
	return nil
}

func (m *Memory) SwitchCutoverImport(_ context.Context, agent definition.GRN, importID, configureRef string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	imp, err := m.cutoverLocked(agent, importID)
	if err != nil {
		return err
	}
	switch imp.Stage {
	case definition.CutoverSwitched:
		return nil
	case definition.CutoverFrozen:
	default:
		return definition.ErrCutoverStage
	}
	imp.Stage, imp.ConfigureRef = definition.CutoverSwitched, configureRef
	m.cutovers[agent] = imp
	m.position++
	m.definitions[agent][0].Assignment = &definition.Assignment{Operator: imp.Assignee, Epoch: imp.Epoch}
	m.positions[agent][0] = m.position
	return nil
}

func (m *Memory) DiscardCutoverImport(_ context.Context, agent definition.GRN, importID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	imp, err := m.cutoverLocked(agent, importID)
	if err != nil {
		return err
	}
	if imp.Stage == definition.CutoverSwitched {
		return definition.ErrReverseMigrationRequired
	}
	delete(m.cutovers, agent)
	delete(m.definitions, agent)
	delete(m.positions, agent)
	return nil
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

func cloneImport(imp definition.CutoverImport) definition.CutoverImport {
	imp.Values = maps.Clone(imp.Values)
	imp.Dispositions = maps.Clone(imp.Dispositions)
	imp.Pins = maps.Clone(imp.Pins)
	return imp
}
