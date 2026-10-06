package definition

import (
	"context"
	"fmt"
)

// PublishProfile publishes settings as the next version of org's named profile, once they are
// settings an agent's workload could run with.
func (s *service) PublishProfile(ctx context.Context, org, name string, settings ExecutionSettings) (Profile, error) {
	if err := settings.check(); err != nil {
		return Profile{}, err
	}
	return s.repository.PublishProfile(ctx, org, name, settings)
}

// PublishProfileVersion publishes p as the version it names of org's profile p.Name, and reports
// whether this call published it. The same settings under a version already published are
// published already; other settings are ErrProfileVersionConflict, and a version neither published
// nor the next is ErrProfileVersionGap (ADR 0056).
func (s *service) PublishProfileVersion(ctx context.Context, org string, p Profile) (Profile, bool, error) {
	switch {
	case org == "" || p.Name == "":
		return Profile{}, false, fmt.Errorf("%w: a profile needs an organization and a name", ErrInvalidProfile)
	case p.Version < 1:
		return Profile{}, false, fmt.Errorf("%w: version %d is below 1", ErrInvalidProfile, p.Version)
	}
	if err := p.Settings.check(); err != nil {
		return Profile{}, false, err
	}
	return s.repository.PublishProfileVersion(ctx, org, p)
}

// Publish publishes in.Template as the next version of its name in the request's organization,
// once per request key: a repeat of the key with the same binding and name returns the version the
// first published, and reports it was not created now; one with another binding or name is
// ErrRequestReused. The profile it names is resolved in that organization only, and a model key
// reference not of the form SecretRef states is ErrInvalidSecretRef, refused before anything is
// stored or read back, as PublishTemplate refuses it.
func (s *service) Publish(ctx context.Context, in PublishInput) (Publication, bool, error) {
	org := in.Request.Organization
	t := in.Template
	if err := t.Config.check(); err != nil {
		return Publication{}, false, err
	}
	if _, err := s.repository.GetProfile(ctx, org, t.Profile); err != nil {
		return Publication{}, false, fmt.Errorf("profile %s version %d: %w", t.Profile.Name, t.Profile.Version, err)
	}
	p := Publication{Key: in.Request, Binding: in.Binding, Template: TemplateRef{Name: t.Name}}
	stored, created, err := s.repository.PublishOnce(ctx, p, t)
	if err != nil {
		return Publication{}, false, err
	}
	if stored.Binding != in.Binding || stored.Template.Name != t.Name {
		return Publication{}, false, ErrRequestReused
	}
	return stored, created, nil
}

// PublishTemplate publishes t as the next version of org's template t.Name. The profile it names
// is resolved in org only, so naming another organization's is ErrNotFound.
func (s *service) PublishTemplate(ctx context.Context, org string, t Template) (Template, error) {
	if err := t.Config.check(); err != nil {
		return Template{}, err
	}
	if _, err := s.repository.GetProfile(ctx, org, t.Profile); err != nil {
		return Template{}, fmt.Errorf("profile %s version %d: %w", t.Profile.Name, t.Profile.Version, err)
	}
	return s.repository.PublishTemplate(ctx, org, t)
}
