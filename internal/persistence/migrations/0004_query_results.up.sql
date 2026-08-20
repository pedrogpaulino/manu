-- Manu query result projection, version 0004.
--
-- Query claims and citations remain normalized in 0002. This table stores the
-- validated response envelope and the package identity needed to reconstruct
-- the synchronous API result without retaining prompts, provider payloads or
-- credentials. The migration runner owns the transaction boundary.

CREATE TABLE query_results (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    query_id uuid NOT NULL,
    package_id uuid NOT NULL,
    package_identity text NOT NULL,
    package_digest char(64) NOT NULL,
    response jsonb NOT NULL,
    response_digest char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT query_results_organization_fk
        FOREIGN KEY (organization_id) REFERENCES organizations (id),
    CONSTRAINT query_results_query_fk
        FOREIGN KEY (organization_id, query_id)
        REFERENCES queries (organization_id, id),
    CONSTRAINT query_results_package_fk
        FOREIGN KEY (organization_id, package_id, query_id)
        REFERENCES evidence_packages (organization_id, id, query_id),
    CONSTRAINT query_results_package_identity_not_blank CHECK (btrim(package_identity) <> ''),
    CONSTRAINT query_results_package_digest_sha256 CHECK (package_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT query_results_response_object CHECK (jsonb_typeof(response) = 'object'),
    CONSTRAINT query_results_response_digest_sha256 CHECK (response_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT query_results_organization_id_unique UNIQUE (organization_id, id),
    CONSTRAINT query_results_query_id_unique UNIQUE (organization_id, query_id)
);
