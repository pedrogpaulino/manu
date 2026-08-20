-- Manu textual evidence projection, version 0003.
--
-- This is a rebuildable projection over authorized Evidence Units. PostgreSQL
-- remains the source of truth in 0001; this table never receives source files
-- or credentials. The simple text-search configuration is explicit and stable
-- so a rebuild has the same tokenization on every installation. The migration
-- runner owns the transaction boundary and records version/checksum together;
-- this script does not begin or commit a transaction.

CREATE TABLE textual_evidence_projection (
    evidence_id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    projection_kind text NOT NULL DEFAULT 'generic',
    content_state text NOT NULL,
    content text NOT NULL,
    content_hash char(64) NOT NULL,
    content_characters bigint NOT NULL,
    truncated boolean NOT NULL DEFAULT false,
    classification text NOT NULL DEFAULT '',
    persist_decision text NOT NULL,
    symbol_name text,
    symbol_qualified_name text,
    configuration_key text,
    exception_type text,
    exact_terms text[] NOT NULL DEFAULT '{}'::text[],
    search_configuration text NOT NULL DEFAULT 'simple',
    search_vector tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple'::regconfig, coalesce(content, '')), 'B')
    ) STORED,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT textual_evidence_projection_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT textual_evidence_projection_evidence_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, evidence_id)
        REFERENCES evidence_units (organization_id, snapshot_id, source_id, id),
    CONSTRAINT textual_evidence_projection_scope_unique UNIQUE (
        organization_id, snapshot_id, source_id, evidence_id
    ),
    CONSTRAINT textual_evidence_projection_kind CHECK (
        projection_kind IN ('generic', 'symbol', 'configuration', 'exception')
    ),
    CONSTRAINT textual_evidence_projection_content_state CHECK (
        content_state IN ('present', 'redacted')
    ),
    CONSTRAINT textual_evidence_projection_content_not_blank CHECK (btrim(content) <> ''),
    CONSTRAINT textual_evidence_projection_content_hash_sha256 CHECK (
        content_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT textual_evidence_projection_content_characters_nonnegative CHECK (
        content_characters >= 0
    ),
    CONSTRAINT textual_evidence_projection_content_length_bounded CHECK (
        char_length(content) <= 16384
    ),
    CONSTRAINT textual_evidence_projection_content_length CHECK (
        char_length(content) = content_characters
    ),
    CONSTRAINT textual_evidence_projection_classification CHECK (
        classification IN ('', 'safe_text', 'sensitive', 'prompt_injection_like', 'binary', 'invalid', 'prohibited')
    ),
    CONSTRAINT textual_evidence_projection_persist_decision CHECK (
        persist_decision IN ('allow', 'redact')
    ),
    CONSTRAINT textual_evidence_projection_redacted_representation CHECK (
        content_state <> 'redacted' OR content = '[redacted]'
    ),
    CONSTRAINT textual_evidence_projection_redacted_decision CHECK (
        persist_decision <> 'redact' OR content_state = 'redacted'
    ),
    CONSTRAINT textual_evidence_projection_restricted_classification CHECK (
        classification NOT IN ('binary', 'invalid', 'prohibited')
    ),
    CONSTRAINT textual_evidence_projection_classification_consistent CHECK (
        classification IN ('', 'safe_text') OR content_state <> 'present'
    ),
    CONSTRAINT textual_evidence_projection_search_configuration CHECK (
        search_configuration = 'simple'
    ),
    CONSTRAINT textual_evidence_projection_symbol_normalized CHECK (
        symbol_name IS NULL OR symbol_name = lower(btrim(symbol_name))
    ),
    CONSTRAINT textual_evidence_projection_qualified_symbol_normalized CHECK (
        symbol_qualified_name IS NULL OR symbol_qualified_name = lower(btrim(symbol_qualified_name))
    ),
    CONSTRAINT textual_evidence_projection_configuration_normalized CHECK (
        configuration_key IS NULL OR configuration_key = lower(btrim(configuration_key))
    ),
    CONSTRAINT textual_evidence_projection_exception_normalized CHECK (
        exception_type IS NULL OR exception_type = lower(btrim(exception_type))
    ),
    CONSTRAINT textual_evidence_projection_organization_id_unique UNIQUE (organization_id, evidence_id)
);

COMMENT ON TABLE textual_evidence_projection IS
    'Rebuildable textual projection of bounded, persistible Evidence Units; it is not canonical knowledge.';
COMMENT ON COLUMN textual_evidence_projection.exact_terms IS
    'Lowercase normalized exact-match terms for symbols, configuration keys, exceptions, and technical terms.';
COMMENT ON COLUMN textual_evidence_projection.search_vector IS
    'Stable simple-config tsvector; derived from bounded authorized content and exact terms.';

CREATE INDEX textual_evidence_projection_scope_idx
    ON textual_evidence_projection (organization_id, source_id, snapshot_id, evidence_id);

CREATE INDEX textual_evidence_projection_search_vector_gin
    ON textual_evidence_projection USING GIN (search_vector);

CREATE INDEX textual_evidence_projection_exact_terms_gin
    ON textual_evidence_projection USING GIN (exact_terms);
