-- Manu factual substrate, version 0005.
--
-- This migration is forward-only and additive. UUID values are supplied by
-- the application; no UUID-generating extension is required. Snapshot-scoped
-- rows retain organization, source, and snapshot in their keys and foreign
-- keys. The migration runner owns the transaction boundary; this script has
-- no transaction-control or destructive statement.
--
-- The minimum cardinality of canonical_fact_inputs for a derived fact cannot
-- be expressed by a row-local CHECK. The task 3.2 repository transaction
-- therefore verifies that every derived fact has at least one input. The
-- schema still prevents inputs for observed facts and keeps each input in the
-- derived fact's rule version and complete snapshot scope.

CREATE TABLE frontend_manifests (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    external_id text NOT NULL,
    manifest_version text NOT NULL,
    frontend_id text NOT NULL,
    version text NOT NULL,
    method text NOT NULL,
    execution_profile text NOT NULL,
    manifest jsonb NOT NULL,
    manifest_digest char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT frontend_manifests_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT frontend_manifests_external_id_not_blank CHECK (btrim(external_id) <> ''),
    CONSTRAINT frontend_manifests_manifest_version_not_blank CHECK (btrim(manifest_version) <> ''),
    CONSTRAINT frontend_manifests_frontend_id_not_blank CHECK (btrim(frontend_id) <> ''),
    CONSTRAINT frontend_manifests_version_not_blank CHECK (btrim(version) <> ''),
    CONSTRAINT frontend_manifests_method_not_blank CHECK (btrim(method) <> ''),
    CONSTRAINT frontend_manifests_execution_profile_not_blank CHECK (btrim(execution_profile) <> ''),
    CONSTRAINT frontend_manifests_manifest_object CHECK (jsonb_typeof(manifest) = 'object'),
    CONSTRAINT frontend_manifests_manifest_digest_sha256 CHECK (
        manifest_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT frontend_manifests_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT frontend_manifests_scope_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT frontend_manifests_external_id_unique UNIQUE (
        organization_id, snapshot_id, source_id, external_id
    ),
    CONSTRAINT frontend_manifests_frontend_identity_unique UNIQUE (
        organization_id, snapshot_id, source_id, frontend_id, version, method
    ),
    CONSTRAINT frontend_manifests_scoped_id_frontend_unique UNIQUE (
        organization_id, snapshot_id, source_id, id, frontend_id, version, method
    )
);

COMMENT ON TABLE frontend_manifests IS
    'Versioned frontend declarations retained per analysis snapshot; manifest identity and digest are append-only application data.';

CREATE TABLE frontend_extension_schemas (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    frontend_manifest_id uuid NOT NULL,
    schema_id text NOT NULL,
    version text NOT NULL,
    digest char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT frontend_extension_schemas_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT frontend_extension_schemas_manifest_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, frontend_manifest_id)
        REFERENCES frontend_manifests (organization_id, snapshot_id, source_id, id),
    CONSTRAINT frontend_extension_schemas_schema_id_not_blank CHECK (btrim(schema_id) <> ''),
    CONSTRAINT frontend_extension_schemas_version_not_blank CHECK (btrim(version) <> ''),
    CONSTRAINT frontend_extension_schemas_digest_sha256 CHECK (
        digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT frontend_extension_schemas_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT frontend_extension_schemas_scope_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT frontend_extension_schemas_identity_unique UNIQUE (
        organization_id, snapshot_id, source_id, frontend_manifest_id,
        schema_id, version
    )
);

COMMENT ON TABLE frontend_extension_schemas IS
    'Extension schema identities declared by a frontend manifest; schema id, version, and digest are append-only application data.';

CREATE TABLE rule_versions (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    rule_id text NOT NULL,
    version text NOT NULL,
    implementation_digest char(64) NOT NULL,
    configuration jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT rule_versions_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT rule_versions_rule_id_not_blank CHECK (btrim(rule_id) <> ''),
    CONSTRAINT rule_versions_version_not_blank CHECK (btrim(version) <> ''),
    CONSTRAINT rule_versions_implementation_digest_sha256 CHECK (
        implementation_digest ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT rule_versions_configuration_object CHECK (jsonb_typeof(configuration) = 'object'),
    CONSTRAINT rule_versions_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT rule_versions_identity_unique UNIQUE (organization_id, rule_id, version)
);

COMMENT ON TABLE rule_versions IS
    'Organization-scoped immutable rule identities; implementation digest and configuration are fixed by identity and repository policy.';

CREATE TABLE canonical_facts (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    identity_key text NOT NULL,
    fact_version text NOT NULL,
    fact_kind text NOT NULL,
    predicate text NOT NULL,
    subject_kind text NOT NULL,
    subject_id text NOT NULL,
    object_kind text,
    object_id text,
    typed_value jsonb,
    frontend_manifest_id uuid,
    producer_id text NOT NULL,
    producer_version text NOT NULL,
    producer_method text NOT NULL,
    rule_version_id uuid,
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT canonical_facts_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT canonical_facts_frontend_fk
        FOREIGN KEY (
            organization_id, snapshot_id, source_id,
            frontend_manifest_id, producer_id, producer_version, producer_method
        )
        REFERENCES frontend_manifests (
            organization_id, snapshot_id, source_id,
            id, frontend_id, version, method
        ) MATCH SIMPLE,
    CONSTRAINT canonical_facts_rule_version_fk
        FOREIGN KEY (organization_id, rule_version_id)
        REFERENCES rule_versions (organization_id, id),
    CONSTRAINT canonical_facts_identity_key_not_blank CHECK (btrim(identity_key) <> ''),
    CONSTRAINT canonical_facts_fact_version_not_blank CHECK (btrim(fact_version) <> ''),
    CONSTRAINT canonical_facts_kind CHECK (fact_kind IN ('observed', 'derived')),
    CONSTRAINT canonical_facts_predicate_not_blank CHECK (btrim(predicate) <> ''),
    CONSTRAINT canonical_facts_subject_kind_not_blank CHECK (btrim(subject_kind) <> ''),
    CONSTRAINT canonical_facts_subject_id_not_blank CHECK (btrim(subject_id) <> ''),
    CONSTRAINT canonical_facts_object_pair CHECK (
        (object_kind IS NULL AND object_id IS NULL)
        OR (
            object_kind IS NOT NULL AND btrim(object_kind) <> ''
            AND object_id IS NOT NULL AND btrim(object_id) <> ''
        )
    ),
    CONSTRAINT canonical_facts_typed_value_object CHECK (
        typed_value IS NULL OR (
            jsonb_typeof(typed_value) = 'object'
            AND jsonb_typeof(typed_value->'kind') = 'string'
            AND btrim(typed_value->>'kind') <> ''
        )
    ),
    CONSTRAINT canonical_facts_object_value_exclusive CHECK (
        NOT ((object_kind IS NOT NULL OR object_id IS NOT NULL) AND typed_value IS NOT NULL)
    ),
    CONSTRAINT canonical_facts_producer_id_not_blank CHECK (btrim(producer_id) <> ''),
    CONSTRAINT canonical_facts_producer_version_not_blank CHECK (btrim(producer_version) <> ''),
    CONSTRAINT canonical_facts_producer_method_not_blank CHECK (btrim(producer_method) <> ''),
    CONSTRAINT canonical_facts_observed_frontend_without_rule CHECK (
        fact_kind <> 'observed'
        OR (frontend_manifest_id IS NOT NULL AND rule_version_id IS NULL)
    ),
    CONSTRAINT canonical_facts_derived_rule_without_frontend CHECK (
        fact_kind <> 'derived'
        OR (frontend_manifest_id IS NULL AND rule_version_id IS NOT NULL)
    ),
    CONSTRAINT canonical_facts_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT canonical_facts_scope_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_facts_scope_kind_rule_unique UNIQUE (
        organization_id, snapshot_id, source_id, id, fact_kind, rule_version_id
    ),
    CONSTRAINT canonical_facts_identity_unique UNIQUE (
        organization_id, snapshot_id, source_id, identity_key
    )
);

COMMENT ON TABLE canonical_facts IS
    'Observed and derived canonical facts. Observed rows have no rule; derived input minimum is checked by the task 3.2 transaction.';

CREATE TABLE canonical_fact_qualifiers (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    fact_id uuid NOT NULL,
    ordinal bigint NOT NULL,
    name text NOT NULL,
    typed_value jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT canonical_fact_qualifiers_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT canonical_fact_qualifiers_fact_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, fact_id)
        REFERENCES canonical_facts (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_fact_qualifiers_ordinal_nonnegative CHECK (ordinal >= 0),
    CONSTRAINT canonical_fact_qualifiers_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT canonical_fact_qualifiers_typed_value_object CHECK (
        jsonb_typeof(typed_value) = 'object'
        AND jsonb_typeof(typed_value->'kind') = 'string'
        AND btrim(typed_value->>'kind') <> ''
    ),
    CONSTRAINT canonical_fact_qualifiers_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT canonical_fact_qualifiers_scope_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_fact_qualifiers_fact_ordinal_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, ordinal
    ),
    CONSTRAINT canonical_fact_qualifiers_fact_name_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, name
    )
);

CREATE TABLE canonical_fact_evidence (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    fact_id uuid NOT NULL,
    evidence_unit_id uuid NOT NULL,
    ordinal bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT canonical_fact_evidence_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT canonical_fact_evidence_fact_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, fact_id)
        REFERENCES canonical_facts (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_fact_evidence_evidence_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, evidence_unit_id)
        REFERENCES evidence_units (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_fact_evidence_ordinal_nonnegative CHECK (ordinal >= 0),
    CONSTRAINT canonical_fact_evidence_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT canonical_fact_evidence_scope_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_fact_evidence_fact_ordinal_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, ordinal
    ),
    CONSTRAINT canonical_fact_evidence_fact_evidence_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, evidence_unit_id
    ),
    CONSTRAINT canonical_fact_evidence_fact_evidence_ordinal_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, evidence_unit_id, ordinal
    )
);

CREATE TABLE canonical_fact_inputs (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    source_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    fact_id uuid NOT NULL,
    input_fact_id uuid NOT NULL,
    rule_version_id uuid NOT NULL,
    fact_kind text NOT NULL DEFAULT 'derived',
    ordinal bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT canonical_fact_inputs_snapshot_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id)
        REFERENCES analysis_snapshots (organization_id, id, source_id),
    CONSTRAINT canonical_fact_inputs_derived_fact_fk
        FOREIGN KEY (
            organization_id, snapshot_id, source_id,
            fact_id, fact_kind, rule_version_id
        )
        REFERENCES canonical_facts (
            organization_id, snapshot_id, source_id,
            id, fact_kind, rule_version_id
        ),
    CONSTRAINT canonical_fact_inputs_input_fact_fk
        FOREIGN KEY (organization_id, snapshot_id, source_id, input_fact_id)
        REFERENCES canonical_facts (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_fact_inputs_rule_version_fk
        FOREIGN KEY (organization_id, rule_version_id)
        REFERENCES rule_versions (organization_id, id),
    CONSTRAINT canonical_fact_inputs_ordinal_nonnegative CHECK (ordinal >= 0),
    CONSTRAINT canonical_fact_inputs_fact_kind CHECK (fact_kind = 'derived'),
    CONSTRAINT canonical_fact_inputs_not_self_link CHECK (fact_id <> input_fact_id),
    CONSTRAINT canonical_fact_inputs_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT canonical_fact_inputs_scope_id_unique UNIQUE (organization_id, snapshot_id, source_id, id),
    CONSTRAINT canonical_fact_inputs_fact_ordinal_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, ordinal
    ),
    CONSTRAINT canonical_fact_inputs_fact_input_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, input_fact_id
    ),
    CONSTRAINT canonical_fact_inputs_fact_input_ordinal_unique UNIQUE (
        organization_id, snapshot_id, source_id, fact_id, input_fact_id, ordinal
    )
);

COMMENT ON TABLE canonical_fact_inputs IS
    'Ordered lineage links for derived facts. The derived fact, its rule version, and each input stay in one snapshot scope; task 3.2 enforces at least one row.';
