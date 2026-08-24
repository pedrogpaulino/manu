package persistence

import (
	"context"
	"fmt"
	"sort"

	"github.com/pedrogpaulino/manu/internal/fact"
)

// FactualLineageEdge identifies one immediate support relationship. FactID is
// the derived fact and InputFactID is one fact used by the rule that produced
// it. The edge intentionally carries the rule identity so an inspection is
// useful when more than one version of a rule has been applied to a snapshot.
type FactualLineageEdge struct {
	FactID      string `json:"fact_id"`
	InputFactID string `json:"input_fact_id"`
	RuleID      string `json:"rule_id"`
	RuleVersion string `json:"rule_version"`
}

// FactualLineageInspection is a detached, deterministic view of one reachable
// part of a factual derivation graph. For support inspection, Facts contains
// the root and all recursively required inputs. For dependent inspection, it
// contains the root input and every recursively derived dependent. Edges keep
// the same orientation in both views: derived FactID -> InputFactID.
type FactualLineageInspection struct {
	Scope fact.Scope           `json:"scope"`
	Root  fact.CanonicalFact   `json:"root"`
	Facts []fact.CanonicalFact `json:"facts"`
	Edges []FactualLineageEdge `json:"edges"`
}

// FactualLineageIndex is an in-memory, snapshot-scoped index over canonical
// facts and their persisted lineage. It owns detached copies of the input and
// never exposes its internal maps or facts. Construct it with
// NewFactualLineageIndex and use InspectSupport or InspectDependents for
// deterministic read-only views.
type FactualLineageIndex struct {
	scope      fact.Scope
	facts      map[string]fact.CanonicalFact
	support    map[string][]FactualLineageEdge
	dependents map[string][]FactualLineageEdge
}

// NewFactualLineageIndex validates and indexes one complete factual snapshot.
// It performs no I/O. The snapshot is prepared through the same canonical
// persistence boundary used for writes and reads, so the input is not mutated
// and invalid references are rejected before an index is returned.
func NewFactualLineageIndex(snapshot FactualSnapshotInput) (FactualLineageIndex, error) {
	prepared, err := prepareFactualSnapshot(snapshot)
	if err != nil {
		return FactualLineageIndex{}, err
	}

	index := FactualLineageIndex{
		scope:      prepared.Scope,
		facts:      make(map[string]fact.CanonicalFact, len(prepared.Facts)),
		support:    make(map[string][]FactualLineageEdge, len(prepared.Facts)),
		dependents: make(map[string][]FactualLineageEdge, len(prepared.Facts)),
	}
	for _, preparedFact := range prepared.Facts {
		if preparedFact.ExternalID == "" || preparedFact.Fact.ID != preparedFact.ExternalID {
			return FactualLineageIndex{}, factualLineageInconsistent()
		}
		if _, exists := index.facts[preparedFact.ExternalID]; exists {
			return FactualLineageIndex{}, factualLineageInconsistent()
		}
		index.facts[preparedFact.ExternalID] = cloneCanonicalFact(preparedFact.Fact)
	}

	for factID, candidate := range index.facts {
		if candidate.Lineage == nil {
			continue
		}
		ruleKey := candidate.Lineage.RuleID + "\x00" + candidate.Lineage.RuleVersion
		if !hasPreparedRule(prepared.RuleVersions, ruleKey) {
			return FactualLineageIndex{}, factualLineageInconsistent()
		}
		for _, inputFactID := range candidate.Lineage.InputFactIDs {
			if _, exists := index.facts[inputFactID]; !exists || inputFactID == factID {
				return FactualLineageIndex{}, factualLineageInconsistent()
			}
			edge := FactualLineageEdge{
				FactID:      factID,
				InputFactID: inputFactID,
				RuleID:      candidate.Lineage.RuleID,
				RuleVersion: candidate.Lineage.RuleVersion,
			}
			index.support[factID] = append(index.support[factID], edge)
			index.dependents[inputFactID] = append(index.dependents[inputFactID], edge)
		}
	}

	for factID := range index.facts {
		sort.Slice(index.support[factID], func(left, right int) bool {
			return factualLineageEdgeLess(index.support[factID][left], index.support[factID][right])
		})
		sort.Slice(index.dependents[factID], func(left, right int) bool {
			return factualLineageEdgeLess(index.dependents[factID][left], index.dependents[factID][right])
		})
	}
	if err := validateFactualLineageAcyclic(index); err != nil {
		return FactualLineageIndex{}, err
	}
	return index, nil
}

// InspectSupport returns the complete recursive support chain rooted at factID
// and ending at observed facts. The root is included even when it is observed.
func (index FactualLineageIndex) InspectSupport(factID string) (FactualLineageInspection, error) {
	return index.inspect(factID, false)
}

// InspectDependents returns the complete recursive reverse closure rooted at
// inputFactID. It uses the prebuilt input-to-dependents map rather than
// rescanning all facts for every recursive step.
func (index FactualLineageIndex) InspectDependents(inputFactID string) (FactualLineageInspection, error) {
	return index.inspect(inputFactID, true)
}

// InspectFactualLineage reads one scoped snapshot and returns the complete
// support chain of factID. ReadFactualSnapshot remains the sole SQL/read
// codec; this method only builds the detached in-memory index above it.
func (r *Repository) InspectFactualLineage(ctx context.Context, organizationID, sourceID, snapshotID, factID string) (FactualLineageInspection, error) {
	return r.inspectFactualLineage(ctx, organizationID, sourceID, snapshotID, factID, false)
}

// InspectFactualDependents reads one scoped snapshot and returns the complete
// recursive set of derived dependents of inputFactID.
func (r *Repository) InspectFactualDependents(ctx context.Context, organizationID, sourceID, snapshotID, inputFactID string) (FactualLineageInspection, error) {
	return r.inspectFactualLineage(ctx, organizationID, sourceID, snapshotID, inputFactID, true)
}

func (r *Repository) inspectFactualLineage(ctx context.Context, organizationID, sourceID, snapshotID, factID string, reverse bool) (FactualLineageInspection, error) {
	if err := validateContext(ctx); err != nil {
		return FactualLineageInspection{}, err
	}
	if err := validateText("factual lineage fact id", factID); err != nil {
		return FactualLineageInspection{}, fmt.Errorf("%w: factual lineage fact id", ErrInvalidInput)
	}
	snapshot, err := r.ReadFactualSnapshot(ctx, organizationID, sourceID, snapshotID)
	if err != nil {
		return FactualLineageInspection{}, err
	}
	index, err := NewFactualLineageIndex(snapshot)
	if err != nil {
		return FactualLineageInspection{}, err
	}
	if reverse {
		return index.InspectDependents(factID)
	}
	return index.InspectSupport(factID)
}

func (index FactualLineageIndex) inspect(rootID string, reverse bool) (FactualLineageInspection, error) {
	if err := validateText("factual lineage fact id", rootID); err != nil {
		return FactualLineageInspection{}, fmt.Errorf("%w: factual lineage fact id", ErrInvalidInput)
	}
	if index.facts == nil || index.support == nil || index.dependents == nil {
		return FactualLineageInspection{}, fmt.Errorf("%w: factual lineage index is not configured", ErrInvalidInput)
	}
	root, exists := index.facts[rootID]
	if !exists {
		return FactualLineageInspection{}, ErrNotFound
	}

	selectedFacts := make(map[string]struct{})
	selectedEdges := make(map[string]FactualLineageEdge)
	visiting := make(map[string]struct{})
	visited := make(map[string]struct{})
	var visit func(string) error
	visit = func(factID string) error {
		if _, done := visited[factID]; done {
			return nil
		}
		if _, active := visiting[factID]; active {
			return factualLineageInconsistent()
		}
		candidate, found := index.facts[factID]
		if !found {
			return factualLineageInconsistent()
		}
		visiting[factID] = struct{}{}
		selectedFacts[factID] = struct{}{}
		edges := index.support[factID]
		if reverse {
			edges = index.dependents[factID]
		}
		for _, edge := range edges {
			selectedEdges[factualLineageEdgeKey(edge)] = edge
			next := edge.InputFactID
			if reverse {
				next = edge.FactID
			}
			if err := visit(next); err != nil {
				return err
			}
		}
		delete(visiting, factID)
		visited[factID] = struct{}{}
		_ = candidate
		return nil
	}
	if err := visit(rootID); err != nil {
		return FactualLineageInspection{}, err
	}

	facts := make([]fact.CanonicalFact, 0, len(selectedFacts))
	for factID := range selectedFacts {
		facts = append(facts, cloneCanonicalFact(index.facts[factID]))
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].ID < facts[right].ID })
	edges := make([]FactualLineageEdge, 0, len(selectedEdges))
	for _, edge := range selectedEdges {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(left, right int) bool { return factualLineageEdgeLess(edges[left], edges[right]) })

	return FactualLineageInspection{
		Scope: index.scope,
		Root:  cloneCanonicalFact(root),
		Facts: facts,
		Edges: edges,
	}, nil
}

func hasPreparedRule(rules []PreparedRuleVersion, key string) bool {
	for _, rule := range rules {
		if rule.RuleID+"\x00"+rule.Version == key {
			return true
		}
	}
	return false
}

func validateFactualLineageAcyclic(index FactualLineageIndex) error {
	state := make(map[string]uint8, len(index.facts))
	factIDs := make([]string, 0, len(index.facts))
	for factID := range index.facts {
		factIDs = append(factIDs, factID)
	}
	sort.Strings(factIDs)
	var visit func(string) error
	visit = func(factID string) error {
		switch state[factID] {
		case 1:
			return factualLineageInconsistent()
		case 2:
			return nil
		}
		if _, exists := index.facts[factID]; !exists {
			return factualLineageInconsistent()
		}
		state[factID] = 1
		for _, edge := range index.support[factID] {
			if err := visit(edge.InputFactID); err != nil {
				return err
			}
		}
		state[factID] = 2
		return nil
	}
	for _, factID := range factIDs {
		if err := visit(factID); err != nil {
			return err
		}
	}
	return nil
}

func factualLineageEdgeKey(edge FactualLineageEdge) string {
	return edge.FactID + "\x00" + edge.InputFactID + "\x00" + edge.RuleID + "\x00" + edge.RuleVersion
}

func factualLineageEdgeLess(left, right FactualLineageEdge) bool {
	if left.FactID != right.FactID {
		return left.FactID < right.FactID
	}
	if left.InputFactID != right.InputFactID {
		return left.InputFactID < right.InputFactID
	}
	if left.RuleID != right.RuleID {
		return left.RuleID < right.RuleID
	}
	return left.RuleVersion < right.RuleVersion
}

func factualLineageInconsistent() error {
	return fmt.Errorf("%w: factual lineage graph is inconsistent", ErrInconsistent)
}
