package repository

const (
	// lockProfiles serializes publications, so two cannot take one version number.
	lockProfiles = `LOCK TABLE profiles IN SHARE ROW EXCLUSIVE MODE`

	publishProfile = `
INSERT INTO profiles (organization, name, version, settings)
SELECT $1::text, $2::text, COALESCE(MAX(version), 0) + 1, $3::jsonb FROM profiles
WHERE organization = $1::text AND name = $2::text
RETURNING version`

	getProfile = `SELECT settings FROM profiles WHERE organization = $1 AND name = $2 AND version = $3`

	latestProfileVersion = `SELECT COALESCE(MAX(version), 0) FROM profiles WHERE organization = $1 AND name = $2`

	insertProfileVersion = `INSERT INTO profiles (organization, name, version, settings) VALUES ($1, $2, $3, $4)`

	lockTemplates = `LOCK TABLE templates IN SHARE ROW EXCLUSIVE MODE`

	publishTemplate = `
INSERT INTO templates (organization, name, version, profile_name, profile_version, config)
SELECT $1::text, $2::text, COALESCE(MAX(version), 0) + 1, $3::text, $4::bigint, $5::jsonb FROM templates
WHERE organization = $1::text AND name = $2::text
RETURNING version`

	getTemplate = `
SELECT profile_name, profile_version, config FROM templates WHERE organization = $1 AND name = $2 AND version = $3`

	// listTemplates is the latest version of each of an organization's templates.
	listTemplates = `
SELECT DISTINCT ON (name) name, version, profile_name, profile_version, config
FROM templates WHERE organization = $1 ORDER BY name, version DESC`

	listProfiles = `SELECT name, version FROM profiles WHERE organization = $1 ORDER BY name, version`

	getPublication = `
SELECT actor, operation, target, body_sha256, operation_ref, template_name, template_version
FROM publications WHERE organization = $1 AND request_id = $2`

	insertPublication = `
INSERT INTO publications (organization, request_id, actor, operation, target, body_sha256, operation_ref,
    template_name, template_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	getStatus = `SELECT observed_revision, rendered_revision, applied_revision FROM agent_status WHERE agent = $1`

	// appendDefinition inserts nothing unless the revision is one past the agent's latest.
	appendDefinition = `
WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, organization, revision, profile_name, profile_version, config, position,
    assignment_operator, assignment_epoch)
SELECT $1::text, $2::text, $3::bigint, $4::text, $5::bigint, $6::jsonb, (SELECT position FROM next), $7::text, $8::text
WHERE (SELECT MAX(revision) FROM definitions WHERE agent = $1::text) = $3::bigint - 1`

	insertFirstDefinition = `
WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, organization, revision, profile_name, profile_version, config, position,
    assignment_operator, assignment_epoch)
SELECT $1::text, $2::text, 1, $3::text, $4::bigint, $5::jsonb, (SELECT position FROM next), $6::text, $7::text`

	getPosition = `SELECT position FROM positions`

	// desired is the latest revision of each agent recorded for an operator, at most $2 of them.
	desired = `
SELECT d.agent, d.organization, d.revision, d.profile_name, d.profile_version, d.config, d.assignment_epoch,
    p.settings, EXISTS (SELECT 1 FROM cutover_imports c WHERE c.agent = d.agent AND c.stage = 'switched')
FROM definitions d
JOIN profiles p ON p.organization = d.organization AND p.name = d.profile_name AND p.version = d.profile_version
WHERE d.assignment_operator = $1
  AND d.revision = (SELECT MAX(revision) FROM definitions latest WHERE latest.agent = d.agent)
ORDER BY d.position
LIMIT $2`

	recordStatus = `
INSERT INTO agent_status (agent, observed_revision, rendered_revision) VALUES ($1, $2, $3)
ON CONFLICT (agent) DO UPDATE SET
    observed_revision = GREATEST(agent_status.observed_revision, EXCLUDED.observed_revision),
    rendered_revision = GREATEST(agent_status.rendered_revision, EXCLUDED.rendered_revision)
RETURNING observed_revision, rendered_revision, applied_revision`

	definitionExists = `SELECT EXISTS (SELECT 1 FROM definitions WHERE agent = $1 AND organization = $2)`

	getDefinition = `
SELECT organization, revision, profile_name, profile_version, config, assignment_operator, assignment_epoch
FROM definitions WHERE agent = $1 ORDER BY revision DESC LIMIT 1`

	beginCreation = `
INSERT INTO creations (organization, request_id, actor, operation, target, body_sha256, operation_ref,
    controller, template_name, template_version, profile_name, profile_version, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'pending')
ON CONFLICT DO NOTHING`

	getCreation = `
SELECT actor, operation, target, body_sha256, operation_ref, controller, template_name, template_version,
    profile_name, profile_version, state, agent, epoch, reason, conflict
FROM creations WHERE organization = $1 AND request_id = $2`

	lockCreation = getCreation + ` FOR UPDATE`

	registerCreation = `
UPDATE creations SET state = 'registered', agent = $3, epoch = $4
WHERE organization = $1 AND request_id = $2`

	failCreation = `
UPDATE creations SET state = 'failed', reason = $3, conflict = $4
WHERE organization = $1 AND request_id = $2 AND state = 'pending'`

	// beginRequest stores a configure request as stale until its revision is stored beside it.
	beginRequest = `
INSERT INTO requests (organization, request_id, actor, operation, target, body_sha256,
    operation_ref, assignment_operator, assignment_epoch, agent, outcome)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'stale')
ON CONFLICT DO NOTHING`

	applyRequest = `
UPDATE requests SET outcome = 'applied', revision = $3
WHERE organization = $1 AND request_id = $2`

	getRequest = `
SELECT actor, operation, target, body_sha256, operation_ref, assignment_operator, assignment_epoch,
    agent, outcome, revision
FROM requests WHERE organization = $1 AND request_id = $2`

	creationOfAgent = `
SELECT organization, request_id FROM creations WHERE agent = $1 AND state = 'registered'`

	// beginCertificate inserts the pending request or returns the stored one in one statement, so
	// a row a refusal removes between an insert and a read is never read as missing.
	beginCertificate = `
INSERT INTO initial_certificates (agent, request_id, epoch, certificate_request_pem, state)
VALUES ($1, $2, $3, $4, 'pending')
ON CONFLICT (agent) DO UPDATE SET agent = EXCLUDED.agent
RETURNING request_id, epoch, certificate_request_pem, state, certificate_pem, issuer_pem, server_root_pem, not_after`

	getCertificate = `
SELECT request_id, epoch, certificate_request_pem, state, certificate_pem, issuer_pem, server_root_pem, not_after
FROM initial_certificates WHERE agent = $1`

	lockCertificate = getCertificate + ` FOR UPDATE`

	issueCertificate = `
UPDATE initial_certificates
SET state = 'issued', certificate_pem = $2, issuer_pem = $3, server_root_pem = $4, not_after = $5
WHERE agent = $1`

	clearCertificate = `
DELETE FROM initial_certificates
WHERE agent = $1 AND state = 'pending' AND request_id = $2 AND epoch = $3 AND certificate_request_pem = $4`

	placementColumns = `controller, epoch, pvc_uid, token_sha256, previous_pod_uid, previous_writer_stopped_sha256,
    leaf_der, revoked_at IS NOT NULL, pod_uid`

	lockCurrentPlacement = `SELECT ` + placementColumns + ` FROM placements
WHERE agent = $1 AND revoked_at IS NULL FOR UPDATE`

	lockPodPlacement = `SELECT ` + placementColumns + ` FROM placements
WHERE agent = $1 AND pod_uid = $2 FOR UPDATE`

	refreshPlacement = `
UPDATE placements SET leaf_der = $3 WHERE agent = $1 AND pod_uid = $2 AND revoked_at IS NULL`

	revokePlacement = `
UPDATE placements SET revoked_at = now() WHERE agent = $1 AND revoked_at IS NULL`

	insertPlacement = `
INSERT INTO placements (agent, pod_uid, controller, epoch, pvc_uid, token_sha256, previous_pod_uid,
    previous_writer_stopped_sha256, leaf_der)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	currentPlacement = `SELECT ` + placementColumns + ` FROM placements
WHERE agent = $1 AND revoked_at IS NULL`

	lockAgent   = `SELECT pg_advisory_lock(hashtextextended('activation:' || $1, 0))`
	unlockAgent = `SELECT pg_advisory_unlock(hashtextextended('activation:' || $1, 0))`

	activationColumns = `request_id, epoch, generation, config_revision, placement_pod_uid,
    replaces_activation_id, operation_ref, COALESCE(activation_id, '')`

	insertActivation = `
INSERT INTO activation_requests (agent, request_id, epoch, generation, config_revision, placement_pod_uid,
    replaces_activation_id, operation_ref)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (agent, request_id) DO UPDATE SET agent = EXCLUDED.agent
RETURNING ` + activationColumns

	getActivation = `SELECT ` + activationColumns + ` FROM activation_requests
WHERE agent = $1 AND request_id = $2`

	recordActivation = `
UPDATE activation_requests SET activation_id = $3
WHERE agent = $1 AND request_id = $2 AND (activation_id IS NULL OR activation_id = $3)`

	recordLatestActivation = `
INSERT INTO agent_activations (agent, latest_activation_id) VALUES ($1, $2)
ON CONFLICT (agent) DO UPDATE SET latest_activation_id = EXCLUDED.latest_activation_id`

	latestActivation = `SELECT latest_activation_id FROM agent_activations WHERE agent = $1`

	activationOfGeneration = `
SELECT activation_id FROM activation_requests
WHERE agent = $1 AND generation = $2 AND activation_id IS NOT NULL LIMIT 1`

	configureReference = `
SELECT operation_ref FROM requests WHERE agent = $1 AND revision = $2 AND outcome = 'applied'`

	recordRuntimeApplied = `
INSERT INTO agent_status (agent, observed_revision, rendered_revision, applied_revision, applied_activation_id,
    applied_generation, applied_observed_at)
VALUES ($1, $2, $2, $2, $3, $4, $5)
ON CONFLICT (agent) DO UPDATE SET applied_revision = EXCLUDED.applied_revision,
    applied_activation_id = EXCLUDED.applied_activation_id, applied_generation = EXCLUDED.applied_generation,
    applied_observed_at = EXCLUDED.applied_observed_at`

	getRuntimeApplied = `
SELECT applied_revision, applied_activation_id, applied_generation, applied_observed_at FROM agent_status
WHERE agent = $1 AND applied_revision IS NOT NULL`

	cutoverColumns = `organization, import_id, epoch, assignee, source_digest, source_values, dispositions, profile_name,
    profile_version, pins, stage, configure_ref`

	lockCutover = `SELECT ` + cutoverColumns + ` FROM cutover_imports WHERE agent = $1 FOR UPDATE`

	getCutover = `SELECT ` + cutoverColumns + ` FROM cutover_imports WHERE agent = $1`

	insertCutover = `
INSERT INTO cutover_imports (agent, organization, import_id, epoch, assignee, source_digest, source_values,
    dispositions, profile_name, profile_version, pins, stage)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'imported')`

	setCutoverStage = `UPDATE cutover_imports SET stage = $2, configure_ref = $3 WHERE agent = $1`

	activateImportedRevision = `
WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
UPDATE definitions SET assignment_operator = $2, assignment_epoch = $3, position = (SELECT position FROM next)
WHERE agent = $1 AND revision = 1 AND assignment_operator IS NULL`

	deleteCutover = `DELETE FROM cutover_imports WHERE agent = $1`

	deleteImportedRevisions = `DELETE FROM definitions WHERE agent = $1`
)
