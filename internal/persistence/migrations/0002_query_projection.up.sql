-- Manu query, projection, and provider schema, version 0002.
--
-- This migration is additive and must be applied after 0001. PostgreSQL is
-- the source of truth; embedding rows are rebuildable projections. UUIDs and
-- hashes are supplied by the application and no raw provider material is
-- persisted in this migration. The migration runner owns the transaction
-- boundary and records the version/checksum in that same transaction; this
-- script does not begin or commit a transaction itself.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE ingestion_jobs (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid,
    snapshot_id uuid,
    source_external_id text NOT NULL,
    snapshot_external_id text NOT NULL,
    factual_digest char(64) NOT NULL,
    analysis_configuration_id text NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    stage text NOT NULL DEFAULT 'validation',
    attempt_count integer NOT NULL DEFAULT 0,
    lease_owner text,
    lease_expires_at timestamptz,
    cancel_requested boolean NOT NULL DEFAULT false,
    diagnostic_code text,
    diagnostic_message text,
    artifact_count bigint NOT NULL DEFAULT 0,
    observation_count bigint NOT NULL DEFAULT 0,
    evidence_count bigint NOT NULL DEFAULT 0,
    failure_count bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at timestamptz,
    finished_at timestamptz,
    CONSTRAINT ingestion_jobs_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT ingestion_jobs_source_fk
        FOREIGN KEY (organization_id, source_id)
        REFERENCES sources (organization_id, id),
    CONSTRAINT ingestion_jobs_snapshot_fk
        FOREIGN KEY (organization_id, source_id, snapshot_id)
        REFERENCES analysis_snapshots (organization_id, source_id, id),
    CONSTRAINT ingestion_jobs_source_external_id_not_blank CHECK (
        btrim(source_external_id) <> ''
    ),
    CONSTRAINT ingestion_jobs_snapshot_external_id_not_blank CHECK (
        btrim(snapshot_external_id) <> ''
    ),
    CONSTRAINT ingestion_jobs_snapshot_scope CHECK (
        snapshot_id IS NULL OR source_id IS NOT NULL
    ),
    CONSTRAINT ingestion_jobs_factual_digest_sha256 CHECK (factual_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ingestion_jobs_configuration_not_blank CHECK (btrim(analysis_configuration_id) <> ''),
    CONSTRAINT ingestion_jobs_state CHECK (
        state IN ('pending', 'running', 'completed', 'partial', 'failed')
    ),
    CONSTRAINT ingestion_jobs_stage CHECK (
        stage IN (
            'validation', 'canonical_persistence', 'textual_projection',
            'relational_projection', 'embedding_projection', 'activation'
        )
    ),
    CONSTRAINT ingestion_jobs_attempt_count_nonnegative CHECK (attempt_count >= 0),
    CONSTRAINT ingestion_jobs_counts_nonnegative CHECK (
        artifact_count >= 0
        AND observation_count >= 0
        AND evidence_count >= 0
        AND failure_count >= 0
    ),
    CONSTRAINT ingestion_jobs_finished_after_started CHECK (
        started_at IS NULL OR finished_at IS NULL OR finished_at >= started_at
    ),
    CONSTRAINT ingestion_jobs_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT ingestion_jobs_factual_identity_unique UNIQUE (
        organization_id, source_external_id, snapshot_external_id, factual_digest
    )
);

COMMENT ON TABLE ingestion_jobs IS
    'Accepted ingestion jobs use external source/snapshot identities before canonical rows are resolved.';

CREATE TABLE ingestion_job_stages (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    job_id uuid NOT NULL,
    stage text NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    lease_owner text,
    lease_expires_at timestamptz,
    item_count bigint NOT NULL DEFAULT 0,
    error_count bigint NOT NULL DEFAULT 0,
    latency_ms bigint NOT NULL DEFAULT 0,
    diagnostic_code text,
    diagnostic_message text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at timestamptz,
    finished_at timestamptz,
    CONSTRAINT ingestion_job_stages_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT ingestion_job_stages_job_fk
        FOREIGN KEY (organization_id, job_id)
        REFERENCES ingestion_jobs (organization_id, id),
    CONSTRAINT ingestion_job_stages_stage CHECK (
        stage IN (
            'validation', 'canonical_persistence', 'textual_projection',
            'relational_projection', 'embedding_projection', 'activation'
        )
    ),
    CONSTRAINT ingestion_job_stages_state CHECK (
        state IN ('pending', 'running', 'completed', 'partial', 'failed')
    ),
    CONSTRAINT ingestion_job_stages_attempt_count_nonnegative CHECK (attempt_count >= 0),
    CONSTRAINT ingestion_job_stages_counts_nonnegative CHECK (
        item_count >= 0 AND error_count >= 0 AND latency_ms >= 0
    ),
    CONSTRAINT ingestion_job_stages_finished_after_started CHECK (
        started_at IS NULL OR finished_at IS NULL OR finished_at >= started_at
    ),
    CONSTRAINT ingestion_job_stages_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT ingestion_job_stages_job_stage_unique UNIQUE (organization_id, job_id, stage)
);

CREATE TABLE embedding_profiles (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    dimension integer NOT NULL,
    normalization text NOT NULL,
    configuration_version text NOT NULL,
    configuration_digest char(64) NOT NULL,
    configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT embedding_profiles_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT embedding_profiles_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT embedding_profiles_model_not_blank CHECK (btrim(model) <> ''),
    CONSTRAINT embedding_profiles_dimension_positive CHECK (dimension > 0),
    CONSTRAINT embedding_profiles_normalization_not_blank CHECK (btrim(normalization) <> ''),
    CONSTRAINT embedding_profiles_configuration_version_not_blank CHECK (
        btrim(configuration_version) <> ''
    ),
    CONSTRAINT embedding_profiles_configuration_digest_sha256 CHECK (
        configuration_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT embedding_profiles_configuration_object CHECK (
        jsonb_typeof(configuration) = 'object'
    ),
    CONSTRAINT embedding_profiles_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT embedding_profiles_dimension_key UNIQUE (organization_id, id, dimension),
    CONSTRAINT embedding_profiles_identity_unique UNIQUE (
        organization_id, provider, model, dimension, normalization,
        configuration_version, configuration_digest
    )
);

COMMENT ON TABLE embedding_profiles IS
    'Embedding profile metadata is append-only by contract; configuration is non-secret and provider keys remain external.';

CREATE TABLE embedding_items (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    profile_id uuid NOT NULL,
    profile_dimension integer NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    evidence_id uuid NOT NULL,
    evidence_content_hash char(64) NOT NULL,
    embedding vector NOT NULL,
    state text NOT NULL DEFAULT 'ready',
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT embedding_items_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT embedding_items_profile_fk
        FOREIGN KEY (organization_id, profile_id, profile_dimension)
        REFERENCES embedding_profiles (organization_id, id, dimension),
    CONSTRAINT embedding_items_evidence_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, evidence_id)
        REFERENCES evidence_units (organization_id, snapshot_id, source_id, id),
    CONSTRAINT embedding_items_profile_dimension_positive CHECK (profile_dimension > 0),
    CONSTRAINT embedding_items_evidence_hash_sha256 CHECK (
        evidence_content_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT embedding_items_dimension_matches_profile CHECK (
        vector_dims(embedding) = profile_dimension
    ),
    CONSTRAINT embedding_items_state CHECK (state IN ('ready', 'stale')),
    CONSTRAINT embedding_items_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT embedding_items_profile_evidence_unique UNIQUE (
        organization_id, profile_id, evidence_id
    )
);

COMMENT ON TABLE embedding_items IS
    'Rebuildable vector projection keyed by an embedding profile and canonical evidence identity.';

CREATE INDEX embedding_items_profile_hash_lookup
    ON embedding_items (organization_id, profile_id, evidence_content_hash);

CREATE TABLE queries (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid,
    snapshot_id uuid,
    embedding_profile_id uuid,
    question text NOT NULL,
    question_digest char(64) NOT NULL,
    retrieval_configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    retrieval_configuration_digest char(64) NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    max_candidates bigint NOT NULL DEFAULT 0,
    max_package_items bigint NOT NULL DEFAULT 0,
    max_package_characters bigint NOT NULL DEFAULT 0,
    max_package_tokens bigint NOT NULL DEFAULT 0,
    max_hops bigint NOT NULL DEFAULT 0,
    max_cost_micros bigint NOT NULL DEFAULT 0,
    candidate_count bigint NOT NULL DEFAULT 0,
    package_count bigint NOT NULL DEFAULT 0,
    latency_ms bigint NOT NULL DEFAULT 0,
    diagnostic_code text,
    diagnostic_message text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at timestamptz,
    finished_at timestamptz,
    CONSTRAINT queries_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT queries_source_fk
        FOREIGN KEY (organization_id, source_id)
        REFERENCES sources (organization_id, id),
    CONSTRAINT queries_snapshot_fk
        FOREIGN KEY (organization_id, source_id, snapshot_id)
        REFERENCES analysis_snapshots (organization_id, source_id, id),
    CONSTRAINT queries_embedding_profile_fk
        FOREIGN KEY (organization_id, embedding_profile_id)
        REFERENCES embedding_profiles (organization_id, id),
    CONSTRAINT queries_question_not_blank CHECK (btrim(question) <> ''),
    CONSTRAINT queries_question_digest_sha256 CHECK (question_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT queries_retrieval_configuration_object CHECK (
        jsonb_typeof(retrieval_configuration) = 'object'
    ),
    CONSTRAINT queries_retrieval_configuration_digest_sha256 CHECK (
        retrieval_configuration_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT queries_scope_consistent CHECK (snapshot_id IS NULL OR source_id IS NOT NULL),
    CONSTRAINT queries_state CHECK (
        state IN ('pending', 'running', 'completed', 'partial', 'failed', 'abstained')
    ),
    CONSTRAINT queries_budgets_nonnegative CHECK (
        max_candidates >= 0
        AND max_package_items >= 0
        AND max_package_characters >= 0
        AND max_package_tokens >= 0
        AND max_hops >= 0
        AND max_cost_micros >= 0
    ),
    CONSTRAINT queries_counts_nonnegative CHECK (
        candidate_count >= 0 AND package_count >= 0 AND latency_ms >= 0
    ),
    CONSTRAINT queries_finished_after_started CHECK (
        started_at IS NULL OR finished_at IS NULL OR finished_at >= started_at
    ),
    CONSTRAINT queries_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT queries_profile_scope_unique UNIQUE (organization_id, id, embedding_profile_id)
);

CREATE TABLE query_candidates (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    query_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    evidence_id uuid NOT NULL,
    embedding_profile_id uuid,
    rank_position integer NOT NULL,
    score numeric NOT NULL DEFAULT 0,
    exact_score numeric,
    lexical_score numeric,
    vector_score numeric,
    relation_score numeric,
    signals jsonb NOT NULL DEFAULT '{}'::jsonb,
    ranking_configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    ranking_configuration_digest char(64) NOT NULL,
    candidate_digest char(64) NOT NULL,
    decision text NOT NULL,
    decision_reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT query_candidates_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT query_candidates_query_fk
        FOREIGN KEY (organization_id, query_id)
        REFERENCES queries (organization_id, id),
    CONSTRAINT query_candidates_query_profile_fk
        FOREIGN KEY (organization_id, query_id, embedding_profile_id)
        REFERENCES queries (organization_id, id, embedding_profile_id),
    CONSTRAINT query_candidates_profile_fk
        FOREIGN KEY (organization_id, embedding_profile_id)
        REFERENCES embedding_profiles (organization_id, id),
    CONSTRAINT query_candidates_evidence_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, evidence_id)
        REFERENCES evidence_units (organization_id, snapshot_id, source_id, id),
    CONSTRAINT query_candidates_rank_positive CHECK (rank_position > 0),
    CONSTRAINT query_candidates_known_scores_nonnegative CHECK (
        score >= 0
        AND (exact_score IS NULL OR exact_score >= 0)
        AND (lexical_score IS NULL OR lexical_score >= 0)
        AND (relation_score IS NULL OR relation_score >= 0)
    ),
    CONSTRAINT query_candidates_signals_object CHECK (jsonb_typeof(signals) = 'object'),
    CONSTRAINT query_candidates_ranking_configuration_object CHECK (
        jsonb_typeof(ranking_configuration) = 'object'
    ),
    CONSTRAINT query_candidates_ranking_configuration_digest_sha256 CHECK (
        ranking_configuration_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT query_candidates_digest_sha256 CHECK (candidate_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT query_candidates_decision CHECK (decision IN ('included', 'excluded')),
    CONSTRAINT query_candidates_decision_reason_not_blank CHECK (btrim(decision_reason) <> ''),
    CONSTRAINT query_candidates_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT query_candidates_query_id_unique UNIQUE (organization_id, query_id, id),
    CONSTRAINT query_candidates_query_evidence_unique UNIQUE (organization_id, query_id, evidence_id),
    CONSTRAINT query_candidates_candidate_scope_unique UNIQUE (
        organization_id, query_id, id, source_id, snapshot_id, evidence_id
    )
);

CREATE TABLE evidence_packages (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    query_id uuid NOT NULL,
    package_digest char(64) NOT NULL,
    selection_configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    selection_configuration_digest char(64) NOT NULL,
    state text NOT NULL DEFAULT 'building',
    max_items bigint NOT NULL DEFAULT 0,
    max_characters bigint NOT NULL DEFAULT 0,
    max_tokens bigint NOT NULL DEFAULT 0,
    candidate_count bigint NOT NULL DEFAULT 0,
    included_count bigint NOT NULL DEFAULT 0,
    excluded_count bigint NOT NULL DEFAULT 0,
    total_characters bigint NOT NULL DEFAULT 0,
    estimated_tokens bigint NOT NULL DEFAULT 0,
    latency_ms bigint NOT NULL DEFAULT 0,
    abstention_reason text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finalized_at timestamptz,
    CONSTRAINT evidence_packages_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT evidence_packages_query_fk
        FOREIGN KEY (organization_id, query_id)
        REFERENCES queries (organization_id, id),
    CONSTRAINT evidence_packages_digest_sha256 CHECK (package_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT evidence_packages_selection_configuration_object CHECK (
        jsonb_typeof(selection_configuration) = 'object'
    ),
    CONSTRAINT evidence_packages_selection_configuration_digest_sha256 CHECK (
        selection_configuration_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT evidence_packages_state CHECK (
        state IN ('building', 'ready', 'partial', 'abstained', 'failed')
    ),
    CONSTRAINT evidence_packages_budgets_nonnegative CHECK (
        max_items >= 0 AND max_characters >= 0 AND max_tokens >= 0
    ),
    CONSTRAINT evidence_packages_counts_nonnegative CHECK (
        candidate_count >= 0
        AND included_count >= 0
        AND excluded_count >= 0
        AND total_characters >= 0
        AND estimated_tokens >= 0
        AND latency_ms >= 0
    ),
    CONSTRAINT evidence_packages_finalized_after_created CHECK (
        finalized_at IS NULL OR finalized_at >= created_at
    ),
    CONSTRAINT evidence_packages_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT evidence_packages_query_id_unique UNIQUE (organization_id, id, query_id),
    CONSTRAINT evidence_packages_query_digest_unique UNIQUE (
        organization_id, query_id, package_digest
    )
);

CREATE TABLE evidence_package_items (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    package_id uuid NOT NULL,
    query_id uuid NOT NULL,
    candidate_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    evidence_id uuid NOT NULL,
    ordinal integer NOT NULL,
    included boolean NOT NULL,
    decision_reason text NOT NULL,
    content_hash char(64) NOT NULL,
    content_characters bigint NOT NULL DEFAULT 0,
    estimated_tokens bigint NOT NULL DEFAULT 0,
    external_transfer_decision text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT evidence_package_items_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT evidence_package_items_package_fk
        FOREIGN KEY (organization_id, package_id, query_id)
        REFERENCES evidence_packages (organization_id, id, query_id),
    CONSTRAINT evidence_package_items_candidate_fk
        FOREIGN KEY (organization_id, query_id, candidate_id)
        REFERENCES query_candidates (organization_id, query_id, id),
    CONSTRAINT evidence_package_items_candidate_scope_fk
        FOREIGN KEY (
            organization_id, query_id, candidate_id, source_id, snapshot_id, evidence_id
        )
        REFERENCES query_candidates (
            organization_id, query_id, id, source_id, snapshot_id, evidence_id
        ),
    CONSTRAINT evidence_package_items_evidence_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, evidence_id)
        REFERENCES evidence_units (organization_id, snapshot_id, source_id, id),
    CONSTRAINT evidence_package_items_ordinal_positive CHECK (ordinal > 0),
    CONSTRAINT evidence_package_items_decision_reason_not_blank CHECK (btrim(decision_reason) <> ''),
    CONSTRAINT evidence_package_items_content_hash_sha256 CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT evidence_package_items_counts_nonnegative CHECK (
        content_characters >= 0 AND estimated_tokens >= 0
    ),
    CONSTRAINT evidence_package_items_transfer_decision CHECK (
        external_transfer_decision IN ('allow', 'redact', 'deny')
    ),
    CONSTRAINT evidence_package_items_included_transfer CHECK (
        NOT included OR external_transfer_decision IN ('allow', 'redact')
    ),
    CONSTRAINT evidence_package_items_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT evidence_package_items_package_evidence_unique UNIQUE (
        organization_id, package_id, evidence_id
    ),
    CONSTRAINT evidence_package_items_citation_key UNIQUE (
        organization_id, package_id, query_id, id, included,
        source_id, snapshot_id, evidence_id
    )
);

CREATE TABLE generated_claims (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    query_id uuid NOT NULL,
    package_id uuid NOT NULL,
    ordinal integer NOT NULL,
    claim_type text NOT NULL,
    claim_text text NOT NULL,
    claim_digest char(64) NOT NULL,
    support_state text NOT NULL,
    confidence numeric,
    citation_count bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT generated_claims_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT generated_claims_query_fk
        FOREIGN KEY (organization_id, query_id)
        REFERENCES queries (organization_id, id),
    CONSTRAINT generated_claims_package_fk
        FOREIGN KEY (organization_id, package_id, query_id)
        REFERENCES evidence_packages (organization_id, id, query_id),
    CONSTRAINT generated_claims_ordinal_positive CHECK (ordinal > 0),
    CONSTRAINT generated_claims_type CHECK (claim_type IN ('observed', 'generated', 'gap')),
    CONSTRAINT generated_claims_text_not_blank CHECK (btrim(claim_text) <> ''),
    CONSTRAINT generated_claims_digest_sha256 CHECK (claim_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT generated_claims_support_state CHECK (
        support_state IN ('supported', 'unsupported', 'abstained')
    ),
    CONSTRAINT generated_claims_confidence_range CHECK (
        confidence IS NULL OR (confidence >= 0 AND confidence <= 1)
    ),
    CONSTRAINT generated_claims_citation_count_nonnegative CHECK (citation_count >= 0),
    CONSTRAINT generated_claims_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT generated_claims_query_package_id_unique UNIQUE (
        organization_id, id, query_id, package_id
    )
);

COMMENT ON TABLE generated_claims IS
    'Generated assertions/claims remain query-scoped output and are not curated knowledge.';

CREATE TABLE citations (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    query_id uuid NOT NULL,
    package_id uuid NOT NULL,
    claim_id uuid NOT NULL,
    package_item_id uuid NOT NULL,
    package_item_included boolean NOT NULL DEFAULT true,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    evidence_id uuid NOT NULL,
    ordinal integer NOT NULL,
    citation_role text NOT NULL,
    locator jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT citations_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT citations_claim_fk
        FOREIGN KEY (organization_id, claim_id, query_id, package_id)
        REFERENCES generated_claims (organization_id, id, query_id, package_id),
    CONSTRAINT citations_included_package_item_fk
        FOREIGN KEY (
            organization_id, package_id, query_id, package_item_id,
            package_item_included, source_id, snapshot_id, evidence_id
        )
        REFERENCES evidence_package_items (
            organization_id, package_id, query_id, id,
            included, source_id, snapshot_id, evidence_id
        ),
    CONSTRAINT citations_package_item_included CHECK (package_item_included),
    CONSTRAINT citations_ordinal_positive CHECK (ordinal > 0),
    CONSTRAINT citations_role CHECK (citation_role IN ('supports', 'contests', 'context')),
    CONSTRAINT citations_locator_object CHECK (jsonb_typeof(locator) = 'object'),
    CONSTRAINT citations_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT citations_claim_item_unique UNIQUE (
        organization_id, claim_id, package_item_id, ordinal
    )
);

CREATE TABLE provider_calls (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    capability text NOT NULL,
    provider text NOT NULL,
    configured_model text NOT NULL,
    effective_model text,
    ingestion_job_id uuid,
    query_id uuid,
    package_id uuid,
    embedding_profile_id uuid,
    request_digest char(64) NOT NULL,
    response_digest char(64),
    transferred_evidence_digest char(64),
    transferred_evidence_count bigint NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'pending',
    error_category text,
    error_code text,
    error_message text,
    attempt_count integer NOT NULL DEFAULT 0,
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    total_tokens bigint NOT NULL DEFAULT 0,
    cost_micros bigint NOT NULL DEFAULT 0,
    latency_ms bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at timestamptz,
    finished_at timestamptz,
    CONSTRAINT provider_calls_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT provider_calls_job_fk
        FOREIGN KEY (organization_id, ingestion_job_id)
        REFERENCES ingestion_jobs (organization_id, id),
    CONSTRAINT provider_calls_query_fk
        FOREIGN KEY (organization_id, query_id)
        REFERENCES queries (organization_id, id),
    CONSTRAINT provider_calls_package_fk
        FOREIGN KEY (organization_id, package_id, query_id)
        REFERENCES evidence_packages (organization_id, id, query_id),
    CONSTRAINT provider_calls_embedding_profile_fk
        FOREIGN KEY (organization_id, embedding_profile_id)
        REFERENCES embedding_profiles (organization_id, id),
    CONSTRAINT provider_calls_capability CHECK (capability IN ('embedding', 'generation')),
    CONSTRAINT provider_calls_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT provider_calls_configured_model_not_blank CHECK (btrim(configured_model) <> ''),
    CONSTRAINT provider_calls_request_digest_sha256 CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT provider_calls_response_digest_sha256 CHECK (
        response_digest IS NULL OR response_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT provider_calls_transferred_digest_sha256 CHECK (
        transferred_evidence_digest IS NULL
        OR transferred_evidence_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT provider_calls_capability_context CHECK (
        (capability = 'embedding' AND embedding_profile_id IS NOT NULL)
        OR (capability = 'generation' AND query_id IS NOT NULL AND embedding_profile_id IS NULL)
    ),
    CONSTRAINT provider_calls_package_scope CHECK (package_id IS NULL OR query_id IS NOT NULL),
    CONSTRAINT provider_calls_state CHECK (
        state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled', 'blocked')
    ),
    CONSTRAINT provider_calls_error_category CHECK (
        error_category IS NULL OR error_category IN (
            'authentication', 'configuration', 'capability', 'budget', 'rate_limit',
            'unavailable', 'timeout', 'cancelled', 'content_blocked',
            'invalid_response', 'unknown'
        )
    ),
    CONSTRAINT provider_calls_failure_diagnostic CHECK (
        state NOT IN ('failed', 'cancelled', 'blocked')
        OR NULLIF(btrim(error_code), '') IS NOT NULL
    ),
    CONSTRAINT provider_calls_counts_nonnegative CHECK (
        transferred_evidence_count >= 0
        AND attempt_count >= 0
        AND input_tokens >= 0
        AND output_tokens >= 0
        AND total_tokens >= 0
        AND cost_micros >= 0
        AND latency_ms >= 0
    ),
    CONSTRAINT provider_calls_finished_after_started CHECK (
        started_at IS NULL OR finished_at IS NULL OR finished_at >= started_at
    ),
    CONSTRAINT provider_calls_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT provider_calls_capability_key UNIQUE (organization_id, id, capability),
    CONSTRAINT provider_calls_attempt_context_key UNIQUE (
        organization_id, id, capability, provider, configured_model
    )
);

CREATE TABLE provider_call_attempts (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    provider_call_id uuid NOT NULL,
    attempt_number integer NOT NULL,
    provider text NOT NULL,
    capability text NOT NULL,
    configured_model text NOT NULL,
    effective_model text,
    request_digest char(64) NOT NULL,
    response_digest char(64),
    transferred_evidence_digest char(64),
    transferred_evidence_count bigint NOT NULL DEFAULT 0,
    state text NOT NULL,
    error_category text,
    error_code text,
    error_message text,
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    total_tokens bigint NOT NULL DEFAULT 0,
    cost_micros bigint NOT NULL DEFAULT 0,
    latency_ms bigint NOT NULL DEFAULT 0,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT provider_call_attempts_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT provider_call_attempts_call_fk
        FOREIGN KEY (organization_id, provider_call_id)
        REFERENCES provider_calls (organization_id, id),
    CONSTRAINT provider_call_attempts_capability_fk
        FOREIGN KEY (organization_id, provider_call_id, capability)
        REFERENCES provider_calls (organization_id, id, capability),
    CONSTRAINT provider_call_attempts_context_fk
        FOREIGN KEY (
            organization_id, provider_call_id, capability, provider, configured_model
        )
        REFERENCES provider_calls (
            organization_id, id, capability, provider, configured_model
        ),
    CONSTRAINT provider_call_attempts_number_positive CHECK (attempt_number > 0),
    CONSTRAINT provider_call_attempts_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT provider_call_attempts_model_not_blank CHECK (btrim(configured_model) <> ''),
    CONSTRAINT provider_call_attempts_capability CHECK (capability IN ('embedding', 'generation')),
    CONSTRAINT provider_call_attempts_request_digest_sha256 CHECK (
        request_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT provider_call_attempts_response_digest_sha256 CHECK (
        response_digest IS NULL OR response_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT provider_call_attempts_transferred_digest_sha256 CHECK (
        transferred_evidence_digest IS NULL
        OR transferred_evidence_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT provider_call_attempts_state CHECK (
        state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled', 'blocked')
    ),
    CONSTRAINT provider_call_attempts_error_category CHECK (
        error_category IS NULL OR error_category IN (
            'authentication', 'configuration', 'capability', 'budget', 'rate_limit',
            'unavailable', 'timeout', 'cancelled', 'content_blocked',
            'invalid_response', 'unknown'
        )
    ),
    CONSTRAINT provider_call_attempts_failure_diagnostic CHECK (
        state NOT IN ('failed', 'cancelled', 'blocked')
        OR NULLIF(btrim(error_code), '') IS NOT NULL
    ),
    CONSTRAINT provider_call_attempts_counts_nonnegative CHECK (
        transferred_evidence_count >= 0
        AND input_tokens >= 0
        AND output_tokens >= 0
        AND total_tokens >= 0
        AND cost_micros >= 0
        AND latency_ms >= 0
    ),
    CONSTRAINT provider_call_attempts_finished_after_started CHECK (
        started_at IS NULL OR finished_at IS NULL OR finished_at >= started_at
    ),
    CONSTRAINT provider_call_attempts_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT provider_call_attempts_call_number_unique UNIQUE (
        organization_id, provider_call_id, attempt_number
    )
);
