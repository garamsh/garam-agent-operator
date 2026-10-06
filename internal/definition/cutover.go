package definition

import (
	"context"
	"errors"
	"maps"
)

// ImportCutover stores a legacy agent's source as its cutover import, with the inactive revision 1
// it makes: the import's profile, resolved in the import's organization only, the pins its dispositions import, no model and no ego, and no
// assignment, so nothing of it is released before garam records the switch. A repeat of the
// stored import is answered with it; any other import for the agent is ErrImportOpen.
func (s *service) ImportCutover(ctx context.Context, imp CutoverImport) (CutoverImport, bool, error) {
	if _, err := s.repository.GetProfile(ctx, imp.Organization, imp.Profile); err != nil {
		return CutoverImport{}, false, err
	}
	imp.Stage = CutoverImported
	d := Definition{
		Agent: imp.Agent, Organization: imp.Organization, Revision: 1, Profile: imp.Profile,
		Config: Configuration{Tools: imp.Pins},
	}
	stored, first, err := s.repository.BeginCutoverImport(ctx, imp, d)
	if err != nil {
		return CutoverImport{}, false, err
	}
	if !sameImport(stored, imp) {
		return CutoverImport{}, false, ErrImportOpen
	}
	return stored, first, nil
}

// sameImport reports whether a stored import is the one asked for again.
func sameImport(stored, asked CutoverImport) bool {
	return stored.Organization == asked.Organization && stored.ImportID == asked.ImportID && stored.Epoch == asked.Epoch && stored.Assignee == asked.Assignee &&
		stored.SourceDigest == asked.SourceDigest && stored.Profile == asked.Profile &&
		maps.Equal(stored.Values, asked.Values) && maps.Equal(stored.Dispositions, asked.Dispositions)
}

// CutoverImportOf returns the agent's cutover import, or ErrNotFound.
func (s *service) CutoverImportOf(ctx context.Context, agent GRN) (CutoverImport, error) {
	return s.repository.GetCutoverImport(ctx, agent)
}

// FreezeCutover records the import frozen, once garam's freeze answered.
func (s *service) FreezeCutover(ctx context.Context, agent GRN, importID string) error {
	return s.repository.FreezeCutoverImport(ctx, agent, importID)
}

// SwitchCutover records the import switched, once garam's switch answered, and in the same step
// activates its revision 1 for the controller the agent is assigned to. configureRef is the
// agent:configure reference revision 1's first activation is sent under.
func (s *service) SwitchCutover(ctx context.Context, agent GRN, importID, configureRef string) error {
	return s.repository.SwitchCutoverImport(ctx, agent, importID, configureRef)
}

// RollBackCutover discards an import that is not switched, with its revision 1, once garam's
// rollback answered.
func (s *service) RollBackCutover(ctx context.Context, agent GRN, importID string) error {
	return s.repository.DiscardCutoverImport(ctx, agent, importID)
}

// cutoverSwitchedOrNone refuses a change to an agent whose cutover import is not switched.
func (s *service) cutoverSwitchedOrNone(ctx context.Context, agent GRN) error {
	imp, err := s.repository.GetCutoverImport(ctx, agent)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if imp.Stage != CutoverSwitched {
		return ErrCutoverPending
	}
	return nil
}

// cutoverReference is the reference an imported revision 1 is activated under: the
// agent:configure reference its switch carried. null is never sent for it (ADR 0050).
func (s *service) cutoverReference(ctx context.Context, agent GRN) (string, error) {
	imp, err := s.repository.GetCutoverImport(ctx, agent)
	if err != nil {
		return "", err
	}
	if imp.Stage != CutoverSwitched || imp.ConfigureRef == "" {
		return "", ErrCutoverPending
	}
	return imp.ConfigureRef, nil
}
