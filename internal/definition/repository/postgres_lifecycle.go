package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func (p *Postgres) OpenRecovery(ctx context.Context, r definition.Recovery) (definition.Recovery, bool, error) {
	var (
		stored definition.Recovery
		first  bool
	)
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		b := r.Binding
		tag, err := tx.Exec(ctx, insertRecovery, string(r.Agent), r.RequestID, r.Key.Organization, r.Key.RequestID,
			b.Actor, b.Operation, b.Target, b.BodySHA256, b.OperationRef, b.Assignment.Operator, b.Assignment.Epoch,
			r.Epoch)
		if err != nil {
			return err
		}
		stored, err = scanRecovery(tx.QueryRow(ctx, getRecoveryByKey, r.Key.Organization, r.Key.RequestID))
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing under the key: the insert met the agent's recovery under the same identifier,
			// or its open one.
			_, err = scanRecovery(tx.QueryRow(ctx, getRecovery, string(r.Agent), r.RequestID))
			if err == nil {
				return definition.ErrRequestReused
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return definition.ErrRecoveryOpen
			}
			return err
		}
		if err != nil {
			return err
		}
		first = tag.RowsAffected() == 1
		if first {
			_, err = tx.Exec(ctx, movePosition)
		}
		return err
	})
	if err != nil {
		return definition.Recovery{}, false, lifecycleError("open recovery", err)
	}
	return stored, first, nil
}

func (p *Postgres) LatestRecovery(ctx context.Context, agent definition.GRN) (definition.Recovery, error) {
	r, err := scanRecovery(p.pool.QueryRow(ctx, latestRecovery, string(agent)))
	if err != nil {
		return definition.Recovery{}, notFound("get latest recovery", err)
	}
	return r, nil
}

func (p *Postgres) PrepareRecovery(
	ctx context.Context, agent definition.GRN, requestID, epoch string, body []byte,
) (definition.Recovery, error) {
	var stored definition.Recovery
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		r, err := scanRecovery(tx.QueryRow(ctx, lockRecovery, string(agent), requestID))
		if errors.Is(err, pgx.ErrNoRows) {
			return definition.ErrNotFound
		}
		if err != nil {
			return err
		}
		switch {
		case r.Epoch != epoch:
			return definition.ErrRecoveryEpoch
		case r.Stage == definition.RecoveryRequested:
			digest := sha256.Sum256(body)
			if _, err := tx.Exec(ctx, prepareRecovery, string(agent), requestID, body,
				hex.EncodeToString(digest[:])); err != nil {
				return err
			}
			r.Stage, r.Body = definition.RecoveryPrepared, body
		case !bytes.Equal(r.Body, body) && r.Stage == definition.RecoveryFinalized:
			return definition.ErrRecoveryStage
		case !bytes.Equal(r.Body, body):
			return definition.ErrRequestReused
		}
		stored = r
		return nil
	})
	if err != nil {
		return definition.Recovery{}, lifecycleError("prepare recovery", err)
	}
	return stored, nil
}

func (p *Postgres) FinalizeRecovery(
	ctx context.Context, agent definition.GRN, requestID string, c definition.RecoveredCredential,
) (definition.Recovery, error) {
	var stored definition.Recovery
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		r, err := scanRecovery(tx.QueryRow(ctx, lockRecovery, string(agent), requestID))
		if errors.Is(err, pgx.ErrNoRows) {
			return definition.ErrNotFound
		}
		if err != nil {
			return err
		}
		switch r.Stage {
		case definition.RecoveryFinalized:
		case definition.RecoveryPrepared:
			if _, err := tx.Exec(ctx, finalizeRecovery, string(agent), requestID, c.Lineage, c.CertificatePEM,
				c.IssuerPEM, c.ServerRootPEM); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, movePosition); err != nil {
				return err
			}
			r.Stage, r.Recovered = definition.RecoveryFinalized, &c
		default:
			return definition.ErrRecoveryStage
		}
		stored = r
		return nil
	})
	if err != nil {
		return definition.Recovery{}, lifecycleError("finalize recovery", err)
	}
	return stored, nil
}

func (p *Postgres) RecordStop(ctx context.Context, s definition.Stop) (definition.Stop, bool, error) {
	var (
		stored definition.Stop
		first  bool
	)
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		b := s.Binding
		tag, err := tx.Exec(ctx, insertStop, s.Key.Organization, s.Key.RequestID, string(s.Agent), b.Actor, b.Operation,
			b.Target, b.BodySHA256, b.OperationRef, b.Assignment.Operator, b.Assignment.Epoch, s.ActivationID)
		if err != nil {
			return err
		}
		stored, err = scanStop(tx.QueryRow(ctx, getStopByKey, s.Key.Organization, s.Key.RequestID))
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing under the key, and the insert met the agent's current stop.
			return definition.ErrAgentStopped
		}
		if err != nil {
			return err
		}
		first = tag.RowsAffected() == 1
		if first {
			_, err = tx.Exec(ctx, movePosition)
		}
		return err
	})
	if err != nil {
		return definition.Stop{}, false, lifecycleError("record stop", err)
	}
	return stored, first, nil
}

func (p *Postgres) RecordDeactivation(ctx context.Context, key definition.RequestKey) error {
	tag, err := p.pool.Exec(ctx, recordDeactivation, key.Organization, key.RequestID)
	if err != nil {
		return storeError("record deactivation", err)
	}
	if tag.RowsAffected() == 0 {
		return definition.ErrNotFound
	}
	return nil
}

func (p *Postgres) RecordStart(ctx context.Context, agent definition.GRN, end definition.StopEnd) (definition.Stop, error) {
	var stored definition.Stop
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		ended, err := scanStop(tx.QueryRow(ctx, getStopByStartKey, end.Key.Organization, end.Key.RequestID))
		if err == nil {
			stored = ended
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		current, err := scanStop(tx.QueryRow(ctx, lockCurrentStop, string(agent)))
		if errors.Is(err, pgx.ErrNoRows) {
			return definition.ErrAgentNotStopped
		}
		if err != nil {
			return err
		}
		b := end.Binding
		if _, err := tx.Exec(ctx, recordStart, current.Key.Organization, current.Key.RequestID, end.Key.RequestID,
			b.Actor, b.Operation, b.Target, b.BodySHA256, b.OperationRef, b.Assignment.Operator,
			b.Assignment.Epoch); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, movePosition); err != nil {
			return err
		}
		current.Start = &end
		stored = current
		return nil
	})
	if err != nil {
		return definition.Stop{}, lifecycleError("record start", err)
	}
	return stored, nil
}

func (p *Postgres) CurrentStop(ctx context.Context, agent definition.GRN) (definition.Stop, error) {
	s, err := scanStop(p.pool.QueryRow(ctx, currentStop, string(agent)))
	if err != nil {
		return definition.Stop{}, notFound("get current stop", err)
	}
	return s, nil
}

// lifecycleError passes the recovery's and the stop's own refusals through, as storeError does
// the store's.
func lifecycleError(op string, err error) error {
	for _, sentinel := range []error{
		definition.ErrRecoveryOpen, definition.ErrRecoveryStage, definition.ErrRecoveryEpoch,
		definition.ErrRequestReused, definition.ErrAgentStopped, definition.ErrAgentNotStopped,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return storeError(op, err)
}

func scanRecovery(row pgx.Row) (definition.Recovery, error) {
	var (
		r                     definition.Recovery
		agent, stage          string
		body                  []byte
		lineage, certPEM      *string
		issuerPEM, serverRoot string
	)
	b := &r.Binding
	if err := row.Scan(&agent, &r.RequestID, &r.Key.Organization, &r.Key.RequestID, &b.Actor, &b.Operation,
		&b.Target, &b.BodySHA256, &b.OperationRef, &b.Assignment.Operator, &b.Assignment.Epoch, &r.Epoch, &stage,
		&body, &lineage, &certPEM, &issuerPEM, &serverRoot); err != nil {
		return definition.Recovery{}, err
	}
	r.Agent, r.Stage, r.Body = definition.GRN(agent), definition.RecoveryStage(stage), body
	if lineage != nil && certPEM != nil {
		r.Recovered = &definition.RecoveredCredential{
			Lineage: *lineage, CertificatePEM: *certPEM, IssuerPEM: issuerPEM, ServerRootPEM: serverRoot,
		}
	}
	return r, nil
}

func scanStop(row pgx.Row) (definition.Stop, error) {
	var (
		s     definition.Stop
		agent string
		start [8]*string
	)
	b := &s.Binding
	if err := row.Scan(&s.Key.Organization, &s.Key.RequestID, &agent, &b.Actor, &b.Operation, &b.Target,
		&b.BodySHA256, &b.OperationRef, &b.Assignment.Operator, &b.Assignment.Epoch, &s.ActivationID, &s.Deactivated,
		&start[0], &start[1], &start[2], &start[3], &start[4], &start[5], &start[6], &start[7]); err != nil {
		return definition.Stop{}, err
	}
	s.Agent = definition.GRN(agent)
	if start[0] != nil {
		value := func(i int) string {
			if start[i] == nil {
				return ""
			}
			return *start[i]
		}
		s.Start = &definition.StopEnd{
			Key: definition.RequestKey{Organization: s.Key.Organization, RequestID: value(0)},
			Binding: definition.Binding{
				Actor: value(1), Operation: value(2), Target: value(3), BodySHA256: value(4), OperationRef: value(5),
				Assignment: definition.Assignment{Operator: value(6), Epoch: value(7)},
			},
		}
	}
	return s, nil
}
