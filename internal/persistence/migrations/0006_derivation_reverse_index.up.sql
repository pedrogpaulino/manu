-- Manu derivation reverse indexes, version 0006.
--
-- This migration is forward-only and additive. The migration runner owns the
-- transaction boundary; this script has no transaction-control or destructive
-- statement. The first index follows canonical_fact_inputs from an input fact
-- to the derived facts that depend on it. The second is a partial lookup for
-- rebuilding facts produced by one rule version within one snapshot.

CREATE INDEX canonical_fact_inputs_input_fact_reverse_lookup
    ON canonical_fact_inputs (
        organization_id,
        source_id,
        snapshot_id,
        input_fact_id,
        fact_id,
        ordinal,
        rule_version_id
    );

COMMENT ON INDEX canonical_fact_inputs_input_fact_reverse_lookup IS
    'Reverse derivation lookup from an input fact to its derived facts within one organization, source, and snapshot.';

CREATE INDEX canonical_facts_derived_rule_snapshot_lookup
    ON canonical_facts (
        organization_id,
        source_id,
        snapshot_id,
        rule_version_id,
        id
    )
    WHERE fact_kind = 'derived';

COMMENT ON INDEX canonical_facts_derived_rule_snapshot_lookup IS
    'Partial lookup for rebuilding derived facts produced by a rule version within one organization, source, and snapshot.';
