package persistence

import (
	"fmt"
	"strings"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

var (
	// ErrInvalidFactualIncremental identifies a malformed incremental plan.
	// It intentionally does not expose a caller-provided ID, path, or payload.
	ErrInvalidFactualIncremental = fmt.Errorf("%w: invalid factual incremental plan", ErrInvalidInput)
)

// FactualSnapshotRevision combines a validated factual snapshot with the
// bounded artifact inventory and effective analyzer configuration that
// produced it. The planner treats both revisions as immutable inputs.
type FactualSnapshotRevision struct {
	Snapshot        FactualSnapshotInput
	Artifacts       []contract.Artifact
	ConfigurationID string
}

// FactualInvalidationReason identifies why a previous factual item cannot be
// reused in an incremental update.
type FactualInvalidationReason string

const (
	FactualInvalidationArtifactHashChanged                      FactualInvalidationReason = "artifact_hash_changed"
	FactualInvalidationArtifactAdded                            FactualInvalidationReason = "artifact_added"
	FactualInvalidationArtifactRemoved                          FactualInvalidationReason = "artifact_removed"
	FactualInvalidationFrontendVersionChanged                   FactualInvalidationReason = "frontend_version_changed"
	FactualInvalidationFrontendManifestChanged                  FactualInvalidationReason = "frontend_manifest_changed"
	FactualInvalidationSchemaDigestChanged                      FactualInvalidationReason = "schema_digest_changed"
	FactualInvalidationConfigurationChanged                     FactualInvalidationReason = "configuration_changed"
	FactualInvalidationObservedChangedOrMissing                 FactualInvalidationReason = "observed_changed_or_missing"
	FactualInvalidationRuleVersionChanged                       FactualInvalidationReason = "rule_version_changed"
	FactualInvalidationRuleImplementationOrConfigurationChanged FactualInvalidationReason = "rule_implementation_or_configuration_changed"
	FactualInvalidationUpstreamLineage                          FactualInvalidationReason = "upstream_lineage"
)

// valid reports whether the reason belongs to the stable incremental
// invalidation vocabulary.
func (r FactualInvalidationReason) valid() bool {
	switch r {
	case FactualInvalidationArtifactHashChanged,
		FactualInvalidationArtifactAdded,
		FactualInvalidationArtifactRemoved,
		FactualInvalidationFrontendVersionChanged,
		FactualInvalidationFrontendManifestChanged,
		FactualInvalidationSchemaDigestChanged,
		FactualInvalidationConfigurationChanged,
		FactualInvalidationObservedChangedOrMissing,
		FactualInvalidationRuleVersionChanged,
		FactualInvalidationRuleImplementationOrConfigurationChanged,
		FactualInvalidationUpstreamLineage:
		return true
	default:
		return false
	}
}

// FactualFactReuse pairs the previous and current snapshot identities of one
// observed fact that can be reused. It carries identities only; the planner
// never copies source content or fact payloads into the delta.
type FactualFactReuse struct {
	PreviousFactID string `json:"previous_fact_id"`
	CurrentFactID  string `json:"current_fact_id"`
}

// FactualInvalidation records the sorted reasons attached to one fact
// identity. FactID may refer to a previous removed/derived fact or to a
// current fact that must be reprocessed.
type FactualInvalidation struct {
	FactID  string                      `json:"fact_id"`
	Reasons []FactualInvalidationReason `json:"reasons"`
}

// FactualSnapshotDelta is the deterministic, content-free plan produced by
// comparing two factual snapshot revisions. Observed reuse pairs refer to
// both snapshots; all other fact ID collections refer to the side named by
// their field.
type FactualSnapshotDelta struct {
	PreviousScope fact.Scope `json:"previous_scope"`
	CurrentScope  fact.Scope `json:"current_scope"`

	ObservedReused       []FactualFactReuse `json:"observed_reused,omitempty"`
	ObservedReprocessIDs []string           `json:"observed_reprocess_ids,omitempty"`
	ObservedRemovedIDs   []string           `json:"observed_removed_ids,omitempty"`
	DerivedReusableIDs   []string           `json:"derived_reusable_ids,omitempty"`
	DerivedReevaluateIDs []string           `json:"derived_reevaluate_ids,omitempty"`

	Invalidations []FactualInvalidation `json:"invalidations,omitempty"`

	ArtifactAddedPaths   []string `json:"artifact_added_paths,omitempty"`
	ArtifactChangedPaths []string `json:"artifact_changed_paths,omitempty"`
	ArtifactRemovedPaths []string `json:"artifact_removed_paths,omitempty"`

	FanoutReevaluated int `json:"fanout_reevaluated"`
}

// Clone returns a defensive copy of the delta and all nested collections.
func (d FactualSnapshotDelta) Clone() FactualSnapshotDelta {
	clone := d
	clone.ObservedReused = append([]FactualFactReuse(nil), d.ObservedReused...)
	clone.ObservedReprocessIDs = append([]string(nil), d.ObservedReprocessIDs...)
	clone.ObservedRemovedIDs = append([]string(nil), d.ObservedRemovedIDs...)
	clone.DerivedReusableIDs = append([]string(nil), d.DerivedReusableIDs...)
	clone.DerivedReevaluateIDs = append([]string(nil), d.DerivedReevaluateIDs...)
	clone.ArtifactAddedPaths = append([]string(nil), d.ArtifactAddedPaths...)
	clone.ArtifactChangedPaths = append([]string(nil), d.ArtifactChangedPaths...)
	clone.ArtifactRemovedPaths = append([]string(nil), d.ArtifactRemovedPaths...)
	if d.Invalidations != nil {
		clone.Invalidations = make([]FactualInvalidation, len(d.Invalidations))
		for index, invalidation := range d.Invalidations {
			clone.Invalidations[index] = invalidation
			clone.Invalidations[index].Reasons = append([]FactualInvalidationReason(nil), invalidation.Reasons...)
		}
	}
	return clone
}

// Validate checks the scope, deterministic ordering, disjoint classifications
// and reason vocabulary of a factual incremental delta. Errors are classified
// without echoing any caller-controlled value.
func (d FactualSnapshotDelta) Validate() error {
	if err := d.PreviousScope.Validate(); err != nil {
		return ErrInvalidFactualIncremental
	}
	if err := d.CurrentScope.Validate(); err != nil {
		return ErrInvalidFactualIncremental
	}
	if d.PreviousScope.OrganizationID != d.CurrentScope.OrganizationID || d.PreviousScope.SourceID != d.CurrentScope.SourceID || d.PreviousScope.SnapshotID == d.CurrentScope.SnapshotID {
		return ErrInvalidFactualIncremental
	}
	if !validateFactReuseCollection(d.ObservedReused) ||
		!validateStringCollection(d.ObservedReprocessIDs) ||
		!validateStringCollection(d.ObservedRemovedIDs) ||
		!validateStringCollection(d.DerivedReusableIDs) ||
		!validateStringCollection(d.DerivedReevaluateIDs) ||
		!validateStringCollection(d.ArtifactAddedPaths) ||
		!validateStringCollection(d.ArtifactChangedPaths) ||
		!validateStringCollection(d.ArtifactRemovedPaths) {
		return ErrInvalidFactualIncremental
	}
	if d.FanoutReevaluated < 0 || d.FanoutReevaluated != len(d.DerivedReevaluateIDs) {
		return ErrInvalidFactualIncremental
	}
	if !validateDisjointClassifications(d) || !validateArtifactClassifications(d) {
		return ErrInvalidFactualIncremental
	}
	if !validateInvalidations(d) {
		return ErrInvalidFactualIncremental
	}
	return nil
}

func validateFactReuseCollection(values []FactualFactReuse) bool {
	for index, value := range values {
		if strings.TrimSpace(value.PreviousFactID) == "" || strings.TrimSpace(value.CurrentFactID) == "" {
			return false
		}
		if index == 0 {
			continue
		}
		previous := values[index-1]
		if previous.PreviousFactID > value.PreviousFactID ||
			(previous.PreviousFactID == value.PreviousFactID && previous.CurrentFactID >= value.CurrentFactID) {
			return false
		}
	}
	return true
}

func validateStringCollection(values []string) bool {
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
		if index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validateDisjointClassifications(delta FactualSnapshotDelta) bool {
	previousReused := make(map[string]struct{}, len(delta.ObservedReused))
	currentReused := make(map[string]struct{}, len(delta.ObservedReused))
	for _, reuse := range delta.ObservedReused {
		if _, exists := previousReused[reuse.PreviousFactID]; exists {
			return false
		}
		if _, exists := currentReused[reuse.CurrentFactID]; exists {
			return false
		}
		previousReused[reuse.PreviousFactID] = struct{}{}
		currentReused[reuse.CurrentFactID] = struct{}{}
	}

	if intersects(currentReused, delta.ObservedReprocessIDs) || intersects(previousReused, delta.ObservedRemovedIDs) {
		return false
	}
	derived := make(map[string]struct{}, len(delta.DerivedReusableIDs)+len(delta.DerivedReevaluateIDs))
	for _, id := range delta.DerivedReusableIDs {
		if _, exists := derived[id]; exists {
			return false
		}
		derived[id] = struct{}{}
	}
	for _, id := range delta.DerivedReevaluateIDs {
		if _, exists := derived[id]; exists {
			return false
		}
		derived[id] = struct{}{}
	}
	return true
}

func validateArtifactClassifications(delta FactualSnapshotDelta) bool {
	seen := make(map[string]struct{}, len(delta.ArtifactAddedPaths)+len(delta.ArtifactChangedPaths)+len(delta.ArtifactRemovedPaths))
	for _, paths := range [][]string{delta.ArtifactAddedPaths, delta.ArtifactChangedPaths, delta.ArtifactRemovedPaths} {
		for _, path := range paths {
			if _, exists := seen[path]; exists {
				return false
			}
			seen[path] = struct{}{}
		}
	}
	return true
}

func validateInvalidations(delta FactualSnapshotDelta) bool {
	classified := make(map[string]struct{}, len(delta.ObservedReprocessIDs)+len(delta.ObservedRemovedIDs)+len(delta.DerivedReevaluateIDs))
	for _, id := range delta.ObservedReprocessIDs {
		classified[id] = struct{}{}
	}
	for _, id := range delta.ObservedRemovedIDs {
		classified[id] = struct{}{}
	}
	for _, id := range delta.DerivedReevaluateIDs {
		classified[id] = struct{}{}
	}

	invalidated := make(map[string]struct{}, len(delta.Invalidations))
	for index, invalidation := range delta.Invalidations {
		if strings.TrimSpace(invalidation.FactID) == "" {
			return false
		}
		invalidated[invalidation.FactID] = struct{}{}
		if _, exists := classified[invalidation.FactID]; !exists {
			return false
		}
		if index > 0 && delta.Invalidations[index-1].FactID >= invalidation.FactID {
			return false
		}
		if len(invalidation.Reasons) == 0 {
			return false
		}
		for reasonIndex, reason := range invalidation.Reasons {
			if !reason.valid() || (reasonIndex > 0 && invalidation.Reasons[reasonIndex-1] >= reason) {
				return false
			}
		}
	}
	if len(invalidated) != len(classified) {
		return false
	}
	for factID := range classified {
		if _, exists := invalidated[factID]; !exists {
			return false
		}
	}
	return true
}

func intersects(values map[string]struct{}, candidates []string) bool {
	for _, candidate := range candidates {
		if _, exists := values[candidate]; exists {
			return true
		}
	}
	return false
}
