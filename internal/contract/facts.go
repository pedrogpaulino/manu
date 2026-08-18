package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// EquivalentFacts compares two results while ignoring execution metadata such
// as run IDs, timestamps, tool versions, and host names. Collection order is
// normalized before comparison.
func EquivalentFacts(left, right Result) bool {
	leftBytes, err := factualJSON(left)
	if err != nil {
		return false
	}
	rightBytes, err := factualJSON(right)
	if err != nil {
		return false
	}
	return string(leftBytes) == string(rightBytes)
}

// FactsEqual is an alias for EquivalentFacts.
func FactsEqual(left, right Result) bool { return EquivalentFacts(left, right) }

// FactualDigest returns a stable SHA-256 digest of the result facts. It does
// not include execution metadata.
func FactualDigest(result Result) (string, error) {
	bytes, err := factualJSON(result)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes)
	return hex.EncodeToString(digest[:]), nil
}

// FactualHash is a convenience form of FactualDigest. Invalid values return
// an empty string; callers that need the reason should use FactualDigest.
func FactualHash(result Result) string {
	digest, err := FactualDigest(result)
	if err != nil {
		return ""
	}
	return digest
}

type factualResult struct {
	ContractVersion string                `json:"contract_version"`
	ResultID        string                `json:"result_id"`
	Source          Source                `json:"source"`
	Snapshot        factualSnapshot       `json:"snapshot"`
	Artifacts       []Artifact            `json:"artifacts"`
	Contributions   []factualContribution `json:"contributions"`
	Coverage        []Coverage            `json:"coverage"`
	Gaps            []Gap                 `json:"gaps"`
	Failures        []Failure             `json:"failures"`
}

type factualSnapshot struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Revision string `json:"revision,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

type factualContribution struct {
	ID              string          `json:"id"`
	ArtifactID      string          `json:"artifact_id"`
	AnalyzerID      string          `json:"analyzer_id"`
	AnalyzerVersion string          `json:"analyzer_version"`
	Method          string          `json:"method"`
	Type            string          `json:"type"`
	Locator         Locator         `json:"locator"`
	Value           json.RawMessage `json:"value,omitempty"`
}

func factualJSON(result Result) ([]byte, error) {
	result = resultCopy(result)
	_ = result.Normalize()
	contributions := make([]factualContribution, len(result.Contributions))
	for i, contribution := range result.Contributions {
		contributions[i] = factualContribution{
			ID:              contribution.ID,
			ArtifactID:      contribution.ArtifactID,
			AnalyzerID:      contribution.AnalyzerID,
			AnalyzerVersion: contribution.AnalyzerVersion,
			Method:          contribution.Method,
			Type:            contribution.Type,
			Locator:         contribution.Locator,
			Value:           append(json.RawMessage(nil), contribution.Value...),
		}
	}
	return json.Marshal(factualResult{
		ContractVersion: result.Manifest.ContractVersion,
		ResultID:        result.Manifest.ResultID,
		Source:          result.Manifest.Source,
		Snapshot: factualSnapshot{
			ID:       result.Manifest.Snapshot.ID,
			SourceID: result.Manifest.Snapshot.SourceID,
			Revision: result.Manifest.Snapshot.Revision,
			Hash:     result.Manifest.Snapshot.Hash,
		},
		Artifacts:     result.Artifacts,
		Contributions: contributions,
		Coverage:      result.Manifest.Coverage,
		Gaps:          result.Manifest.Gaps,
		Failures:      result.Manifest.Failures,
	})
}

func resultCopy(result Result) Result {
	result.Artifacts = append([]Artifact(nil), result.Artifacts...)
	result.Contributions = append([]Contribution(nil), result.Contributions...)
	result.Manifest.Coverage = append([]Coverage(nil), result.Manifest.Coverage...)
	result.Manifest.Gaps = append([]Gap(nil), result.Manifest.Gaps...)
	result.Manifest.Failures = append([]Failure(nil), result.Manifest.Failures...)
	for i := range result.Manifest.Coverage {
		if result.Manifest.Coverage[i].Locator != nil {
			locator := *result.Manifest.Coverage[i].Locator
			result.Manifest.Coverage[i].Locator = &locator
		}
	}
	for i := range result.Manifest.Gaps {
		if result.Manifest.Gaps[i].Locator != nil {
			locator := *result.Manifest.Gaps[i].Locator
			result.Manifest.Gaps[i].Locator = &locator
		}
	}
	for i := range result.Manifest.Failures {
		if result.Manifest.Failures[i].Locator != nil {
			locator := *result.Manifest.Failures[i].Locator
			result.Manifest.Failures[i].Locator = &locator
		}
	}
	return result
}
