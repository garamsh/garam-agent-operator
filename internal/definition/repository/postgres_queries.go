package repository

const (
	// lockProfiles serializes publications, so two cannot take one version number.
	lockProfiles = `LOCK TABLE profiles IN SHARE ROW EXCLUSIVE MODE`

	publishProfile = `
INSERT INTO profiles (name, version, settings)
SELECT $1::text, COALESCE(MAX(version), 0) + 1, $2::jsonb FROM profiles WHERE name = $1::text
RETURNING version`

	getProfile = `SELECT settings FROM profiles WHERE name = $1 AND version = $2`

	lockTemplates = `LOCK TABLE templates IN SHARE ROW EXCLUSIVE MODE`

	publishTemplate = `
INSERT INTO templates (name, version, profile_name, profile_version, config)
SELECT $1::text, COALESCE(MAX(version), 0) + 1, $2::text, $3::bigint, $4::jsonb FROM templates WHERE name = $1::text
RETURNING version`

	getTemplate = `
SELECT profile_name, profile_version, config FROM templates WHERE name = $1 AND version = $2`

	// appendDefinition inserts nothing unless the revision is one past the agent's latest.
	appendDefinition = `
WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, revision, profile_name, profile_version, config, position,
    assignment_operator, assignment_epoch)
SELECT $1::text, $2::bigint, $3::text, $4::bigint, $5::jsonb, (SELECT position FROM next), $6::text, $7::text
WHERE (SELECT MAX(revision) FROM definitions WHERE agent = $1::text) = $2::bigint - 1`

	insertFirstDefinition = `
WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, revision, profile_name, profile_version, config, position,
    assignment_operator, assignment_epoch)
SELECT $1::text, 1, $2::text, $3::bigint, $4::jsonb, (SELECT position FROM next), $5::text, $6::text`

	getPosition = `SELECT position FROM positions`

	// desired is the latest revision of each agent recorded for an operator, stored after a position.
	desired = `
SELECT d.agent, d.revision, d.profile_name, d.profile_version, d.config, d.assignment_epoch, p.settings
FROM definitions d
JOIN profiles p ON p.name = d.profile_name AND p.version = d.profile_version
WHERE d.position > $2 AND d.assignment_operator = $1
  AND d.revision = (SELECT MAX(revision) FROM definitions latest WHERE latest.agent = d.agent)
ORDER BY d.position`

	recordStatus = `
INSERT INTO agent_status (agent, observed_revision, rendered_revision) VALUES ($1, $2, $3)
ON CONFLICT (agent) DO UPDATE SET
    observed_revision = GREATEST(agent_status.observed_revision, EXCLUDED.observed_revision),
    rendered_revision = GREATEST(agent_status.rendered_revision, EXCLUDED.rendered_revision)
RETURNING observed_revision, rendered_revision, applied_revision`

	definitionExists = `SELECT EXISTS (SELECT 1 FROM definitions WHERE agent = $1)`

	getDefinition = `
SELECT revision, profile_name, profile_version, config, assignment_operator, assignment_epoch FROM definitions
WHERE agent = $1 ORDER BY revision DESC LIMIT 1`

	beginCreation = `
INSERT INTO creations (organization, request_id, actor, template_name, template_version, state)
VALUES ($1, $2, $3, $4, $5, 'pending')
ON CONFLICT DO NOTHING`

	getCreation = `
SELECT actor, template_name, template_version, state, agent, reason FROM creations
WHERE organization = $1 AND request_id = $2`

	lockCreation = getCreation + ` FOR UPDATE`

	registerCreation = `
UPDATE creations SET state = 'registered', agent = $3
WHERE organization = $1 AND request_id = $2`

	failCreation = `
UPDATE creations SET state = 'failed', reason = $3
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
)
