-- +goose Up
ALTER TABLE deployments ADD COLUMN change_proposal_id TEXT;
CREATE UNIQUE INDEX deployments_change_proposal_idx ON deployments(change_proposal_id) WHERE change_proposal_id IS NOT NULL;

CREATE TABLE assistant_model_providers (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    api_url TEXT NOT NULL,
    model TEXT NOT NULL,
    api_key_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    allow_private INTEGER NOT NULL DEFAULT 0 CHECK(allow_private IN (0, 1)),
    status TEXT NOT NULL CHECK(status IN ('configured', 'verified', 'failed')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE assistant_conversations (
    id TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX assistant_conversations_admin_idx ON assistant_conversations(admin_id, updated_at DESC);

CREATE TABLE assistant_messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES assistant_conversations(id) ON DELETE CASCADE,
    run_id TEXT,
    role TEXT NOT NULL CHECK(role IN ('user', 'assistant', 'system')),
    content TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX assistant_messages_conversation_idx ON assistant_messages(conversation_id, created_at, id);

CREATE TABLE assistant_runs (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES assistant_conversations(id) ON DELETE CASCADE,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK(status IN ('queued', 'running', 'completed', 'failed', 'cancelled', 'approval_required')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX assistant_runs_conversation_idx ON assistant_runs(conversation_id, created_at DESC);

CREATE TABLE assistant_tool_calls (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES assistant_runs(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    arguments_json BLOB NOT NULL,
    result_json BLOB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK(status IN ('running', 'completed', 'failed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE change_proposals (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES assistant_conversations(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES assistant_runs(id) ON DELETE CASCADE,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind = 'install_application'),
    request_json BLOB NOT NULL,
    summary_json BLOB NOT NULL,
    digest TEXT NOT NULL,
    targets_json BLOB NOT NULL,
    expected_revision TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    risk TEXT NOT NULL CHECK(risk IN ('low', 'medium', 'high')),
    status TEXT NOT NULL CHECK(status IN ('pending', 'approved', 'rejected', 'expired', 'applied', 'cancelled')),
    expires_at TEXT NOT NULL,
    deployment_id TEXT REFERENCES deployments(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX change_proposals_conversation_idx ON change_proposals(conversation_id, created_at DESC);

CREATE TABLE change_approvals (
    id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES change_proposals(id) ON DELETE CASCADE,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    decision TEXT NOT NULL CHECK(decision IN ('approved', 'rejected')),
    digest TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(proposal_id, admin_id, decision)
);
CREATE INDEX change_approvals_proposal_idx ON change_approvals(proposal_id, created_at);

CREATE TABLE assistant_audit_events (
    id TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
    conversation_id TEXT REFERENCES assistant_conversations(id) ON DELETE SET NULL,
    run_id TEXT REFERENCES assistant_runs(id) ON DELETE SET NULL,
    tool_call_id TEXT REFERENCES assistant_tool_calls(id) ON DELETE SET NULL,
    proposal_id TEXT REFERENCES change_proposals(id) ON DELETE SET NULL,
    deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    kind TEXT NOT NULL,
    payload_json BLOB NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX assistant_audit_events_created_idx ON assistant_audit_events(created_at DESC);

CREATE TABLE assistant_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL REFERENCES assistant_conversations(id) ON DELETE CASCADE,
    run_id TEXT REFERENCES assistant_runs(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    data_json BLOB NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX assistant_events_conversation_idx ON assistant_events(conversation_id, id);

PRAGMA user_version = 41;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
