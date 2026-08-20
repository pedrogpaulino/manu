-- Manu canonical knowledge schema, version 0001.
--
-- This is a forward-only, additive migration. UUID values are supplied by
-- the application; no UUID-generating extension is required. The
-- organization_id component is intentionally present in every relationship
-- so a valid identifier from another organization cannot satisfy a foreign
-- key accidentally. The migration runner owns the transaction boundary and
-- records the version/checksum in that same transaction; this script does not
-- begin or commit a transaction itself.

CREATE TABLE organizations (
    id uuid PRIMARY KEY,
    external_id text NOT NULL,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT organizations_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT organizations_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT organizations_external_id_unique UNIQUE (external_id)
);

CREATE TABLE sources (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    external_id text NOT NULL,
    name text NOT NULL,
    source_type text NOT NULL,
    root text,
    active_snapshot_id uuid,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT sources_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT sources_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT sources_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT sources_type_not_blank CHECK (btrim(source_type) <> ''),
    CONSTRAINT sources_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT sources_external_id_unique UNIQUE (organization_id, external_id)
);

CREATE TABLE analysis_snapshots (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    external_id text NOT NULL,
    source_revision text,
    source_hash char(64),
    analysis_configuration_id text NOT NULL,
    factual_digest char(64) NOT NULL,
    captured_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT analysis_snapshots_source_fk
        FOREIGN KEY (organization_id, source_id)
        REFERENCES sources (organization_id, id),
    CONSTRAINT analysis_snapshots_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT analysis_snapshots_configuration_not_blank CHECK (btrim(analysis_configuration_id) <> ''),
    CONSTRAINT analysis_snapshots_source_identity_present CHECK (
        NULLIF(btrim(source_revision), '') IS NOT NULL
        OR source_hash IS NOT NULL
    ),
    CONSTRAINT analysis_snapshots_source_hash_sha256 CHECK (
        source_hash IS NULL OR source_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT analysis_snapshots_factual_digest_sha256 CHECK (
        factual_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT analysis_snapshots_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT analysis_snapshots_source_id_unique UNIQUE (organization_id, source_id, id),
    CONSTRAINT analysis_snapshots_source_scope_unique UNIQUE (organization_id, id, source_id),
    CONSTRAINT analysis_snapshots_external_id_unique UNIQUE (organization_id, source_id, external_id)
);

-- The composite key includes source_id as well as organization_id. A source
-- therefore cannot activate a snapshot belonging to another source, even if
-- both rows use the same local snapshot identifier.
ALTER TABLE sources
    ADD CONSTRAINT sources_active_snapshot_fk
    FOREIGN KEY (organization_id, id, active_snapshot_id)
    REFERENCES analysis_snapshots (organization_id, source_id, id);

COMMENT ON TABLE analysis_snapshots IS
    'Append-only analysis snapshots; historical rows remain queryable when a source advances. This migration avoids cascades and updated_at; repository transactions enforce write immutability in the persistence layer.';
COMMENT ON COLUMN sources.active_snapshot_id IS
    'Nullable pointer to the current snapshot; its composite FK enforces organization and source scope.';

CREATE TABLE artifacts (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    external_id text NOT NULL,
    path text NOT NULL,
    artifact_type text NOT NULL,
    content_hash char(64) NOT NULL,
    content_size bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT artifacts_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT artifacts_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT artifacts_path_not_blank CHECK (btrim(path) <> ''),
    CONSTRAINT artifacts_type_not_blank CHECK (btrim(artifact_type) <> ''),
    CONSTRAINT artifacts_content_hash_sha256 CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT artifacts_content_size_nonnegative CHECK (content_size >= 0),
    CONSTRAINT artifacts_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT artifacts_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT artifacts_external_id_unique UNIQUE (organization_id, snapshot_id, external_id),
    CONSTRAINT artifacts_path_hash_unique UNIQUE (organization_id, snapshot_id, path, content_hash)
);

CREATE TABLE observations (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    external_id text NOT NULL,
    analyzer_id text NOT NULL,
    analyzer_version text NOT NULL,
    method text NOT NULL,
    observation_type text NOT NULL,
    locator jsonb NOT NULL,
    value jsonb,
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT observations_artifact_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, artifact_id)
        REFERENCES artifacts (organization_id, snapshot_id, source_id, id),
    CONSTRAINT observations_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT observations_analyzer_id_not_blank CHECK (btrim(analyzer_id) <> ''),
    CONSTRAINT observations_analyzer_version_not_blank CHECK (btrim(analyzer_version) <> ''),
    CONSTRAINT observations_method_not_blank CHECK (btrim(method) <> ''),
    CONSTRAINT observations_type_not_blank CHECK (btrim(observation_type) <> ''),
    CONSTRAINT observations_locator_object CHECK (jsonb_typeof(locator) = 'object'),
    CONSTRAINT observations_value_shape CHECK (
        value IS NULL OR jsonb_typeof(value) IN ('object', 'array', 'string', 'number', 'boolean', 'null')
    ),
    CONSTRAINT observations_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT observations_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT observations_external_id_unique UNIQUE (organization_id, snapshot_id, external_id)
);

COMMENT ON TABLE observations IS
    'Canonical Observation/Contribution facts produced by an analyzer for one Artifact.';

CREATE TABLE entities (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    external_id text NOT NULL,
    entity_type text NOT NULL,
    name text,
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT entities_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT entities_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT entities_type_not_blank CHECK (btrim(entity_type) <> ''),
    CONSTRAINT entities_attributes_object CHECK (jsonb_typeof(attributes) = 'object'),
    CONSTRAINT entities_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT entities_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT entities_external_id_unique UNIQUE (organization_id, snapshot_id, external_id)
);

CREATE TABLE relationships (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    external_id text NOT NULL,
    from_entity_id uuid NOT NULL,
    to_entity_id uuid NOT NULL,
    relationship_type text NOT NULL,
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT relationships_source_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT relationships_from_entity_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, from_entity_id)
        REFERENCES entities (organization_id, snapshot_id, source_id, id),
    CONSTRAINT relationships_to_entity_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, to_entity_id)
        REFERENCES entities (organization_id, snapshot_id, source_id, id),
    CONSTRAINT relationships_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT relationships_type_not_blank CHECK (btrim(relationship_type) <> ''),
    CONSTRAINT relationships_attributes_object CHECK (jsonb_typeof(attributes) = 'object'),
    CONSTRAINT relationships_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT relationships_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT relationships_external_id_unique UNIQUE (organization_id, snapshot_id, external_id),
    CONSTRAINT relationships_directed_unique UNIQUE (
        organization_id, snapshot_id, source_id, from_entity_id, to_entity_id, relationship_type
    )
);

CREATE TABLE evidence_units (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    observation_id uuid,
    external_id text NOT NULL,
    locator jsonb NOT NULL,
    content_state text NOT NULL,
    content text,
    content_hash char(64) NOT NULL,
    content_bytes bigint NOT NULL DEFAULT 0,
    content_characters bigint NOT NULL DEFAULT 0,
    truncated boolean NOT NULL DEFAULT false,
    classification text NOT NULL DEFAULT '',
    findings jsonb NOT NULL DEFAULT '[]'::jsonb,
    persist_decision text NOT NULL,
    external_transfer_decision text NOT NULL,
    redaction_reason text,
    provenance jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT evidence_units_artifact_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, artifact_id)
        REFERENCES artifacts (organization_id, snapshot_id, source_id, id),
    CONSTRAINT evidence_units_observation_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, observation_id)
        REFERENCES observations (organization_id, snapshot_id, source_id, id),
    CONSTRAINT evidence_units_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT evidence_units_locator_object CHECK (jsonb_typeof(locator) = 'object'),
    CONSTRAINT evidence_units_content_state CHECK (content_state IN ('present', 'redacted', 'omitted')),
    CONSTRAINT evidence_units_content_hash_sha256 CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT evidence_units_content_bytes_nonnegative CHECK (content_bytes >= 0),
    CONSTRAINT evidence_units_content_characters_nonnegative CHECK (content_characters >= 0),
    CONSTRAINT evidence_units_content_counts CHECK (
        (content IS NULL AND content_bytes = 0 AND content_characters = 0)
        OR (
            content IS NOT NULL
            AND octet_length(content) = content_bytes
            AND char_length(content) = content_characters
        )
    ),
    CONSTRAINT evidence_units_content_state_consistent CHECK (
        (content_state = 'present' AND content IS NOT NULL)
        OR (
            content_state = 'redacted'
            AND content = '[redacted]'
            AND redaction_reason IS NOT NULL
            AND btrim(redaction_reason) <> ''
        )
        OR (
            content_state = 'omitted'
            AND content IS NULL
            AND content_bytes = 0
            AND content_characters = 0
        )
    ),
    CONSTRAINT evidence_units_present_reason_check CHECK (
        content_state <> 'present'
        OR redaction_reason IS NULL
    ),
    CONSTRAINT evidence_units_classification CHECK (
        -- Empty classification is retained only for explicitly authorized
        -- legacy rows; the transfer constraint below keeps it non-transferable.
        classification IN ('', 'safe_text', 'sensitive', 'prompt_injection_like', 'binary', 'invalid', 'prohibited')
    ),
    CONSTRAINT evidence_units_findings_array CHECK (jsonb_typeof(findings) = 'array'),
    CONSTRAINT evidence_units_persist_decision CHECK (persist_decision IN ('allow', 'redact', 'deny')),
    CONSTRAINT evidence_units_transfer_decision CHECK (external_transfer_decision IN ('allow', 'redact', 'deny')),
    CONSTRAINT evidence_units_persist_consistent CHECK (
        persist_decision <> 'deny'
        OR (content_state = 'omitted' AND content IS NULL AND content_bytes = 0 AND content_characters = 0)
    ),
    CONSTRAINT evidence_units_redaction_consistent CHECK (
        persist_decision <> 'redact' OR content_state <> 'present'
    ),
    CONSTRAINT evidence_units_classification_consistent CHECK (
        classification IN ('', 'safe_text') OR content_state <> 'present'
    ),
    CONSTRAINT evidence_units_restricted_classification_check CHECK (
        classification NOT IN ('binary', 'invalid', 'prohibited')
        OR content_state = 'omitted'
    ),
    CONSTRAINT evidence_units_transfer_consistent CHECK (
        external_transfer_decision <> 'allow' OR classification = 'safe_text'
    ),
    CONSTRAINT evidence_units_provenance_object CHECK (jsonb_typeof(provenance) = 'object'),
    CONSTRAINT evidence_units_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT evidence_units_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT evidence_units_external_id_unique UNIQUE (organization_id, snapshot_id, external_id)
);

COMMENT ON TABLE evidence_units IS
    'Authorized, bounded Evidence Units; source files and credentials are not stored here.';

CREATE TABLE analysis_coverage (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    external_id text NOT NULL,
    dimension text NOT NULL,
    scope text,
    state text NOT NULL,
    analyzer_id text,
    message text,
    locator jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT analysis_coverage_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT analysis_coverage_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT analysis_coverage_dimension_not_blank CHECK (btrim(dimension) <> ''),
    CONSTRAINT analysis_coverage_state CHECK (
        state IN ('produced', 'incomplete', 'not_supported', 'not_applicable', 'failed')
    ),
    CONSTRAINT analysis_coverage_locator_object CHECK (
        locator IS NULL OR jsonb_typeof(locator) = 'object'
    ),
    CONSTRAINT analysis_coverage_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT analysis_coverage_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT analysis_coverage_external_id_unique UNIQUE (organization_id, snapshot_id, external_id)
);

CREATE TABLE explicit_gaps (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    external_id text NOT NULL,
    code text NOT NULL,
    dimension text,
    scope text,
    message text NOT NULL,
    analyzer_id text,
    locator jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT explicit_gaps_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT explicit_gaps_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT explicit_gaps_code_not_blank CHECK (btrim(code) <> ''),
    CONSTRAINT explicit_gaps_message_not_blank CHECK (btrim(message) <> ''),
    CONSTRAINT explicit_gaps_locator_object CHECK (
        locator IS NULL OR jsonb_typeof(locator) = 'object'
    ),
    CONSTRAINT explicit_gaps_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT explicit_gaps_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT explicit_gaps_external_id_unique UNIQUE (organization_id, snapshot_id, external_id)
);

CREATE TABLE analysis_failures (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    artifact_id uuid,
    external_id text NOT NULL,
    code text NOT NULL,
    operation text NOT NULL,
    message text NOT NULL,
    analyzer_id text,
    partial boolean NOT NULL DEFAULT false,
    locator jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT analysis_failures_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT analysis_failures_artifact_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, artifact_id)
        REFERENCES artifacts (organization_id, snapshot_id, source_id, id),
    CONSTRAINT analysis_failures_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT analysis_failures_code_not_blank CHECK (btrim(code) <> ''),
    CONSTRAINT analysis_failures_operation_not_blank CHECK (btrim(operation) <> ''),
    CONSTRAINT analysis_failures_message_not_blank CHECK (btrim(message) <> ''),
    CONSTRAINT analysis_failures_locator_object CHECK (
        locator IS NULL OR jsonb_typeof(locator) = 'object'
    ),
    CONSTRAINT analysis_failures_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT analysis_failures_snapshot_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT analysis_failures_external_id_unique UNIQUE (organization_id, snapshot_id, external_id)
);

-- One row per factual identity/snapshot keeps historical identities while a
-- partial unique index guarantees one active identity for each source key.
CREATE TABLE factual_identities (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    identity_key text NOT NULL,
    factual_digest char(64) NOT NULL,
    state text NOT NULL DEFAULT 'historical',
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT factual_identities_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT factual_identities_identity_key_not_blank CHECK (btrim(identity_key) <> ''),
    CONSTRAINT factual_identities_digest_sha256 CHECK (factual_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT factual_identities_state CHECK (state IN ('active', 'historical')),
    CONSTRAINT factual_identities_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT factual_identities_snapshot_identity_unique UNIQUE (
        organization_id, source_id, snapshot_id, identity_key
    )
);

CREATE UNIQUE INDEX factual_identities_one_active_per_source_key
    ON factual_identities (organization_id, source_id, identity_key)
    WHERE state = 'active';

CREATE INDEX factual_identities_digest_lookup
    ON factual_identities (organization_id, source_id, factual_digest, identity_key);

COMMENT ON TABLE factual_identities IS
    'Active and historical factual identities; historical rows are retained for idempotency and comparison.';
