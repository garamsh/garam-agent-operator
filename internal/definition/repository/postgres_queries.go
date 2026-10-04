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
INSERT INTO definitions (agent, revision, profile_name, profile_version, config)
SELECT $1::text, $2::bigint, $3::text, $4::bigint, $5::jsonb
WHERE (SELECT MAX(revision) FROM definitions WHERE agent = $1::text) = $2::bigint - 1`

	insertFirstDefinition = `
INSERT INTO definitions (agent, revision, profile_name, profile_version, config)
VALUES ($1, 1, $2, $3, $4)`

	definitionExists = `SELECT EXISTS (SELECT 1 FROM definitions WHERE agent = $1)`

	getDefinition = `
SELECT revision, profile_name, profile_version, config FROM definitions
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
