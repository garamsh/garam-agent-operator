package repository

import (
	"bytes"
	"context"
	"slices"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func (m *Memory) OpenRecovery(_ context.Context, r definition.Recovery) (definition.Recovery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, recoveries := range m.recoveries {
		for _, stored := range recoveries {
			if stored.Key == r.Key {
				return cloneRecovery(stored), false, nil
			}
		}
	}
	if m.recoveryLocked(r.Agent, r.RequestID) >= 0 {
		return definition.Recovery{}, false, definition.ErrRequestReused
	}
	if m.openRecoveryLocked(r.Agent) != nil {
		return definition.Recovery{}, false, definition.ErrRecoveryOpen
	}
	m.recoveries[r.Agent] = append(m.recoveries[r.Agent], cloneRecovery(r))
	m.position++
	return cloneRecovery(r), true, nil
}

func (m *Memory) LatestRecovery(_ context.Context, agent definition.GRN) (definition.Recovery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	recoveries := m.recoveries[agent]
	if len(recoveries) == 0 {
		return definition.Recovery{}, definition.ErrNotFound
	}
	return cloneRecovery(recoveries[len(recoveries)-1]), nil
}

func (m *Memory) PrepareRecovery(
	_ context.Context, agent definition.GRN, requestID, epoch string, body []byte,
) (definition.Recovery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.recoveryLocked(agent, requestID)
	if i < 0 {
		return definition.Recovery{}, definition.ErrNotFound
	}
	r := &m.recoveries[agent][i]
	if r.Epoch != epoch {
		return definition.Recovery{}, definition.ErrRecoveryEpoch
	}
	switch {
	case r.Stage == definition.RecoveryRequested:
		r.Stage, r.Body = definition.RecoveryPrepared, slices.Clone(body)
	case !bytes.Equal(r.Body, body) && r.Stage == definition.RecoveryFinalized:
		return definition.Recovery{}, definition.ErrRecoveryStage
	case !bytes.Equal(r.Body, body):
		return definition.Recovery{}, definition.ErrRequestReused
	}
	return cloneRecovery(*r), nil
}

func (m *Memory) FinalizeRecovery(
	_ context.Context, agent definition.GRN, requestID string, c definition.RecoveredCredential,
) (definition.Recovery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.recoveryLocked(agent, requestID)
	if i < 0 {
		return definition.Recovery{}, definition.ErrNotFound
	}
	r := &m.recoveries[agent][i]
	switch r.Stage {
	case definition.RecoveryFinalized:
	case definition.RecoveryPrepared:
		r.Stage, r.Recovered = definition.RecoveryFinalized, &c
		m.position++
	default:
		return definition.Recovery{}, definition.ErrRecoveryStage
	}
	return cloneRecovery(*r), nil
}

func (m *Memory) RecordStop(_ context.Context, s definition.Stop) (definition.Stop, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.stops[s.Key]; ok {
		return cloneStop(stored), false, nil
	}
	if m.currentStopLocked(s.Agent) != nil {
		return definition.Stop{}, false, definition.ErrAgentStopped
	}
	s.Deactivated, s.Start = false, nil
	m.stops[s.Key] = s
	m.position++
	return cloneStop(s), true, nil
}

func (m *Memory) RecordDeactivation(_ context.Context, key definition.RequestKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.stops[key]
	if !ok {
		return definition.ErrNotFound
	}
	stored.Deactivated = true
	m.stops[key] = stored
	return nil
}

func (m *Memory) RecordStart(_ context.Context, agent definition.GRN, end definition.StopEnd) (definition.Stop, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stopKey, ok := m.stopEnds[end.Key]; ok {
		return cloneStop(m.stops[stopKey]), nil
	}
	current := m.currentStopLocked(agent)
	if current == nil {
		return definition.Stop{}, definition.ErrAgentNotStopped
	}
	stored := *current
	stored.Start = &end
	m.stops[stored.Key] = stored
	m.stopEnds[end.Key] = stored.Key
	m.position++
	return cloneStop(stored), nil
}

func (m *Memory) CurrentStop(_ context.Context, agent definition.GRN) (definition.Stop, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.currentStopLocked(agent)
	if current == nil {
		return definition.Stop{}, definition.ErrNotFound
	}
	return cloneStop(*current), nil
}

// currentStopLocked is the stop holding the agent, nil where none does.
func (m *Memory) currentStopLocked(agent definition.GRN) *definition.Stop {
	for _, s := range m.stops {
		if s.Agent == agent && s.Start == nil {
			return &s
		}
	}
	return nil
}

// openRecoveryLocked is what the agent's open recovery is prepared under, nil where none is open.
func (m *Memory) openRecoveryLocked(agent definition.GRN) *definition.OpenRecovery {
	for _, r := range m.recoveries[agent] {
		if r.Stage != definition.RecoveryFinalized {
			return &definition.OpenRecovery{RequestID: r.RequestID, Epoch: r.Epoch}
		}
	}
	return nil
}

// recoveryLocked is the index of the agent's recovery requestID, -1 where there is none.
func (m *Memory) recoveryLocked(agent definition.GRN, requestID string) int {
	return slices.IndexFunc(m.recoveries[agent], func(r definition.Recovery) bool { return r.RequestID == requestID })
}

func cloneRecovery(r definition.Recovery) definition.Recovery {
	r.Body = slices.Clone(r.Body)
	if r.Recovered != nil {
		recovered := *r.Recovered
		r.Recovered = &recovered
	}
	return r
}

func cloneStop(s definition.Stop) definition.Stop {
	if s.Start != nil {
		end := *s.Start
		s.Start = &end
	}
	return s
}
