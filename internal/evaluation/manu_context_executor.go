package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/query"
)

const (
	// ManuContextGenerationNotExecuted records that this adapter measures
	// context assembly only. It never invokes a model or produces an answer.
	ManuContextGenerationNotExecuted = DirectSourceGenerationNotExecuted
	// ManuContextContentFreeResult records that this adapter returns only a
	// content-free result projection; it does not expose context bytes as
	// source-file bytes.
	ManuContextContentFreeResult = "content_free_result"
	// ManuContextTypedTargetNotAvailable records that the current evaluation
	// task contract does not expose a canonical entity or symbol target.
	ManuContextTypedTargetNotAvailable = "typed_target_not_available"

	defaultManuContextOrganization = defaultEvaluationOrganization
)

var (
	// ErrInvalidManuContextExecutor identifies missing dependencies, an
	// invalid explicit budget, or an invalid fixed organization binding.
	ErrInvalidManuContextExecutor = errors.New("evaluation: invalid manu-context executor")
	// ErrManuContextUnavailable identifies a resolver or ContextService that
	// could not provide a package. Backend details never cross this boundary.
	ErrManuContextUnavailable = errors.New("evaluation: manu-context unavailable")
	// ErrManuContextInvalidPackage identifies a package that crossed the
	// service boundary without satisfying the query contract.
	ErrManuContextInvalidPackage = errors.New("evaluation: invalid manu-context package")
	// ErrManuContextScopeMismatch identifies a package returned for a scope
	// different from the one resolved for the evaluation source revision.
	ErrManuContextScopeMismatch = errors.New("evaluation: manu-context scope mismatch")
)

// EvaluationScopeResolver maps the external evaluation organization, source
// identity and source revision to one immutable query scope. Implementations
// own the mapping policy; this adapter does not import persistence or CLI.
type EvaluationScopeResolver interface {
	Resolve(context.Context, string, string, string) (query.Scope, error)
}

// EvaluationScopeResolverFunc adapts a function to EvaluationScopeResolver.
type EvaluationScopeResolverFunc func(context.Context, string, string, string) (query.Scope, error)

// Resolve implements EvaluationScopeResolver.
func (f EvaluationScopeResolverFunc) Resolve(
	ctx context.Context,
	organizationExternal string,
	sourceID string,
	sourceRevision string,
) (query.Scope, error) {
	if f == nil {
		return query.Scope{}, ErrInvalidManuContextExecutor
	}
	return f(ctx, organizationExternal, sourceID, sourceRevision)
}

var _ EvaluationScopeResolver = EvaluationScopeResolverFunc(nil)

// ManuContextExecutorConfig contains the real read-only ContextService port,
// the scope mapping port and the explicit context budget. OrganizationExternal
// is fixed for the executor and never copied to a result or error.
type ManuContextExecutorConfig struct {
	Service              query.ContextService
	Resolver             EvaluationScopeResolver
	OrganizationExternal string
	Limits               query.ContextLimits
}

// ManuContextConfig is a descriptive alias for ManuContextExecutorConfig.
type ManuContextConfig = ManuContextExecutorConfig

// ManuContextExecutor invokes the application ContextService once per
// evaluation request and returns only a content-free package projection.
type ManuContextExecutor struct {
	service              query.ContextService
	resolver             EvaluationScopeResolver
	organizationExternal string
	limits               query.ContextLimits
}

var _ VariantExecutor = (*ManuContextExecutor)(nil)

// NewManuContextExecutor constructs a manu-context executor with the
// repository's fixed synthetic evaluation organization. The service, resolver
// and limits are intentionally explicit; no runtime defaults are inferred.
func NewManuContextExecutor(
	service query.ContextService,
	resolver EvaluationScopeResolver,
	limits query.ContextLimits,
) (*ManuContextExecutor, error) {
	return NewManuContextExecutorWithOrganization(
		service,
		resolver,
		defaultManuContextOrganization,
		limits,
	)
}

// NewManuContextExecutorWithOrganization constructs an executor for one
// explicitly fixed external organization identity.
func NewManuContextExecutorWithOrganization(
	service query.ContextService,
	resolver EvaluationScopeResolver,
	organizationExternal string,
	limits query.ContextLimits,
) (*ManuContextExecutor, error) {
	if isNilManuContextDependency(service) || isNilManuContextDependency(resolver) {
		return nil, ErrInvalidManuContextExecutor
	}
	if strings.TrimSpace(organizationExternal) == "" || organizationExternal != strings.TrimSpace(organizationExternal) {
		return nil, ErrInvalidManuContextExecutor
	}
	if err := limits.Validate(); err != nil {
		return nil, ErrInvalidManuContextExecutor
	}
	return &ManuContextExecutor{
		service:              service,
		resolver:             resolver,
		organizationExternal: organizationExternal,
		limits:               limits,
	}, nil
}

// NewConfiguredManuContextExecutor is the configuration-shaped constructor
// for callers that need a non-default fixed external organization.
func NewConfiguredManuContextExecutor(config ManuContextExecutorConfig) (*ManuContextExecutor, error) {
	return NewManuContextExecutorWithOrganization(
		config.Service,
		config.Resolver,
		config.OrganizationExternal,
		config.Limits,
	)
}

// Validate verifies the immutable executor dependencies and explicit budget.
func (e *ManuContextExecutor) Validate() error {
	if e == nil || isNilManuContextDependency(e.service) || isNilManuContextDependency(e.resolver) {
		return ErrInvalidManuContextExecutor
	}
	if e.organizationExternal == "" || e.organizationExternal != strings.TrimSpace(e.organizationExternal) {
		return ErrInvalidManuContextExecutor
	}
	if err := e.limits.Validate(); err != nil {
		return ErrInvalidManuContextExecutor
	}
	return nil
}

// Execute resolves one scope and invokes BuildContext exactly once. The
// returned result remains limited because generation is intentionally absent.
func (e *ManuContextExecutor) Execute(
	ctx context.Context,
	request VariantExecutionRequest,
) (VariantExecutionResult, error) {
	if ctx == nil {
		return VariantExecutionResult{}, ErrInvalidVariantRequest
	}
	if err := ctx.Err(); err != nil {
		return VariantExecutionResult{}, err
	}
	if err := e.Validate(); err != nil {
		return VariantExecutionResult{}, err
	}
	if err := request.Validate(); err != nil || request.Variant.Kind != VariantManuContext || !manuContextPolicyAllowed(request.Policy) {
		return VariantExecutionResult{}, ErrInvalidVariantRequest
	}

	started := time.Now()
	scope, err := e.resolver.Resolve(
		ctx,
		e.organizationExternal,
		request.SourceID,
		request.SourceRevision,
	)
	if err != nil {
		return VariantExecutionResult{}, manuContextDependencyError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return VariantExecutionResult{}, err
	}
	if err := scope.Validate(); err != nil || scope.SourceID == "" || scope.SnapshotID == "" {
		return VariantExecutionResult{}, ErrManuContextUnavailable
	}

	contextRequest, err := manuContextRequest(request, scope, e.limits)
	if err != nil {
		return VariantExecutionResult{}, err
	}
	packageContext, err := e.service.BuildContext(ctx, contextRequest)
	if err != nil {
		return VariantExecutionResult{}, manuContextDependencyError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return VariantExecutionResult{}, err
	}
	if err := packageContext.Validate(); err != nil {
		return VariantExecutionResult{}, ErrManuContextInvalidPackage
	}
	if packageContext.Scope != scope {
		return VariantExecutionResult{}, ErrManuContextScopeMismatch
	}

	matched := manuContextMatchedEvidence(request.Case.ExpectedEvidence, packageContext.Items)
	projection, encoded, err := manuContextSafeProjection(request, packageContext, matched)
	if err != nil {
		return VariantExecutionResult{}, ErrManuContextInvalidPackage
	}
	digest := sha256.Sum256(encoded)
	_ = projection

	bytesRead := packageContext.BytesUsed
	toolCalls := int64(1)
	limitations := []string{
		ManuContextGenerationNotExecuted,
		ManuContextContentFreeResult,
	}
	if request.Task.Kind == TaskKindImpact {
		limitations = append(limitations, ManuContextTypedTargetNotAvailable)
	}
	return VariantExecutionResult{
		Version:      VariantExecutionVersion,
		Status:       VariantStatusLimited,
		Conclusion:   VariantConclusionPartial,
		OutputDigest: hex.EncodeToString(digest[:]),
		EvidenceIDs:  matched,
		Limitations:  limitations,
		Metrics: &VariantMetrics{
			ObserverID:      "manu-context-executor",
			ObserverVersion: VariantExecutionVersion,
			ToolCalls:       &toolCalls,
			BytesRead:       &bytesRead,
			Duration:        manuContextDuration(time.Since(started)),
		},
	}, nil
}

func isNilManuContextDependency(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func manuContextPolicyAllowed(policy EvaluationPolicy) bool {
	readOnlySource := policy.SourceAccess == "read-only" || policy.SourceAccess == "context-read-only"
	if !readOnlySource || policy.ExternalTransfer != "deny" || policy.NetworkAccess != "disabled" || policy.MutationAccess != "disabled" {
		return false
	}
	for _, permission := range policy.Permissions {
		if permission == "context.read" {
			return true
		}
	}
	return false
}

func manuContextDependencyError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrManuContextUnavailable
}

func manuContextRequest(
	request VariantExecutionRequest,
	scope query.Scope,
	limits query.ContextLimits,
) (query.ContextRequest, error) {
	intent, err := manuContextIntent(request)
	if err != nil {
		return query.ContextRequest{}, err
	}
	contextRequest := query.ContextRequest{
		Version: query.ContextVersion,
		Scope:   scope,
		Intent:  intent,
		Limits:  limits,
	}
	if err := contextRequest.Validate(); err != nil {
		return query.ContextRequest{}, ErrInvalidVariantRequest
	}
	return contextRequest, nil
}

func manuContextIntent(request VariantExecutionRequest) (query.Intent, error) {
	if request.Task.Kind != TaskKindLocalization && request.Task.Kind != TaskKindExplanation && request.Task.Kind != TaskKindImpact {
		return query.Intent{}, ErrInvalidVariantRequest
	}
	// EvaluationTask intentionally has no canonical entity/symbol target.
	// Keep retrieval driven by the competence question only; expected evidence
	// and reference answers are evaluation material, not query input.
	intent := query.Intent{
		Version:  query.ContextVersion,
		Kind:     query.IntentKindQuestion,
		Question: request.Case.CompetenceQuestion,
	}
	if err := intent.Validate(); err != nil {
		return query.Intent{}, ErrInvalidVariantRequest
	}
	return intent, nil
}

func manuContextMatchedEvidence(expected []ExpectedEvidence, items []query.ContextItem) []string {
	type candidateMatch struct {
		itemIndex int
		score     int
	}
	adjacency := make([][]candidateMatch, len(expected))
	for expectedIndex, candidate := range expected {
		if candidate.EvidenceID == "" || candidate.Locator == nil {
			continue
		}
		for itemIndex, item := range items {
			locator := manuContextItemLocator(item)
			if locator != nil && manuContextLocatorMatches(*candidate.Locator, *locator) {
				adjacency[expectedIndex] = append(adjacency[expectedIndex], candidateMatch{
					itemIndex: itemIndex,
					score:     manuContextLocatorMatchScore(*candidate.Locator, *locator),
				})
			}
		}
		sort.SliceStable(adjacency[expectedIndex], func(left, right int) bool {
			if adjacency[expectedIndex][left].score != adjacency[expectedIndex][right].score {
				return adjacency[expectedIndex][left].score > adjacency[expectedIndex][right].score
			}
			return adjacency[expectedIndex][left].itemIndex < adjacency[expectedIndex][right].itemIndex
		})
	}

	matchedExpectedByItem := make([]int, len(items))
	for index := range matchedExpectedByItem {
		matchedExpectedByItem[index] = -1
	}
	var augment func(int, []bool) bool
	augment = func(expectedIndex int, visitedItems []bool) bool {
		for _, candidate := range adjacency[expectedIndex] {
			if visitedItems[candidate.itemIndex] {
				continue
			}
			visitedItems[candidate.itemIndex] = true
			matchedExpected := matchedExpectedByItem[candidate.itemIndex]
			if matchedExpected == -1 || augment(matchedExpected, visitedItems) {
				matchedExpectedByItem[candidate.itemIndex] = expectedIndex
				return true
			}
		}
		return false
	}
	for expectedIndex := range adjacency {
		augment(expectedIndex, make([]bool, len(items)))
	}

	matched := make([]string, 0, len(expected))
	for _, expectedIndex := range matchedExpectedByItem {
		if expectedIndex >= 0 {
			matched = append(matched, expected[expectedIndex].EvidenceID)
		}
	}
	sort.Strings(matched)
	return manuContextUniqueStrings(matched)
}

func manuContextItemLocator(item query.ContextItem) *contract.Locator {
	if err := item.Locator.Validate(); err == nil {
		locator := item.Locator
		return &locator
	}
	if item.Evidence != nil {
		if err := item.Evidence.Locator.Validate(); err == nil {
			locator := item.Evidence.Locator
			return &locator
		}
	}
	if item.Fact != nil && len(item.Fact.Evidence) > 0 {
		if err := item.Fact.Evidence[0].Locator.Validate(); err == nil {
			locator := item.Fact.Evidence[0].Locator
			return &locator
		}
	}
	return nil
}

func manuContextLocatorMatches(expected, actual contract.Locator) bool {
	if expected.Validate() != nil || actual.Validate() != nil {
		return false
	}
	if !manuContextPathMatches(expected.Path, actual.Path) {
		return false
	}
	if !manuContextHasMeaningfulDimension(expected) || !manuContextHasMeaningfulDimension(actual) {
		return false
	}
	if !manuContextLocatorDimensionMatches(expected.URI, actual.URI) ||
		!manuContextLocatorDimensionMatches(expected.SourceID, actual.SourceID) ||
		!manuContextLocatorDimensionMatches(expected.ArtifactID, actual.ArtifactID) {
		return false
	}
	if expected.Member != "" && actual.Member != "" && expected.Member != actual.Member {
		return false
	}
	comparableDimension := expected.Member != "" && actual.Member != ""
	if manuContextHasLinePosition(expected) && manuContextHasLinePosition(actual) &&
		!manuContextLineRangesOverlap(expected, actual) {
		return false
	}
	if manuContextHasLinePosition(expected) && manuContextHasLinePosition(actual) {
		comparableDimension = true
	}
	if manuContextHasColumnPosition(expected) && manuContextHasColumnPosition(actual) &&
		!manuContextColumnRangesOverlap(expected, actual) {
		return false
	}
	if manuContextHasColumnPosition(expected) && manuContextHasColumnPosition(actual) {
		comparableDimension = true
	}
	if manuContextHasBytePosition(expected) && manuContextHasBytePosition(actual) &&
		!manuContextByteRangesOverlap(expected, actual) {
		return false
	}
	if manuContextHasBytePosition(expected) && manuContextHasBytePosition(actual) {
		comparableDimension = true
	}
	return comparableDimension
}

func manuContextLocatorMatchScore(expected, actual contract.Locator) int {
	score := manuContextPathMatchScore(expected.Path, actual.Path)
	if expected.Member != "" && actual.Member != "" {
		score += 40
	} else if expected.Member != "" || actual.Member != "" {
		score += 5
	}
	if expected.URI != "" && actual.URI != "" {
		score += 20
	}
	if expected.SourceID != "" && actual.SourceID != "" {
		score += 20
	}
	if expected.ArtifactID != "" && actual.ArtifactID != "" {
		score += 20
	}
	score += manuContextLineRangeMatchScore(expected, actual)
	score += manuContextColumnRangeMatchScore(expected, actual)
	score += manuContextByteRangeMatchScore(expected, actual)
	return score
}

func manuContextPathMatchScore(expected, actual string) int {
	if expected == "" || actual == "" {
		return 0
	}
	normalize := func(value string) string {
		return path.Clean(strings.ReplaceAll(value, "\\", "/"))
	}
	normalizedExpected := normalize(expected)
	normalizedActual := normalize(actual)
	if normalizedExpected == normalizedActual {
		return 100
	}
	if !strings.Contains(normalizedExpected, "/") || !strings.Contains(normalizedActual, "/") {
		if path.Base(normalizedExpected) == path.Base(normalizedActual) {
			return 50
		}
	}
	return 0
}

func manuContextLineRangeMatchScore(expected, actual contract.Locator) int {
	if !manuContextHasLinePosition(expected) || !manuContextHasLinePosition(actual) {
		return 0
	}
	expectedStart, expectedEnd := manuContextLineRange(expected)
	actualStart, actualEnd := manuContextLineRange(actual)
	return manuContextRangeMatchScore(expectedStart, expectedEnd, actualStart, actualEnd)
}

func manuContextColumnRangeMatchScore(expected, actual contract.Locator) int {
	if !manuContextHasColumnPosition(expected) || !manuContextHasColumnPosition(actual) {
		return 0
	}
	expectedStart, expectedEnd := manuContextColumnRange(expected)
	actualStart, actualEnd := manuContextColumnRange(actual)
	return manuContextRangeMatchScore(expectedStart, expectedEnd, actualStart, actualEnd)
}

func manuContextByteRangeMatchScore(expected, actual contract.Locator) int {
	if !manuContextHasBytePosition(expected) || !manuContextHasBytePosition(actual) {
		return 0
	}
	expectedStart, expectedEnd := manuContextByteRange(expected)
	actualStart, actualEnd := manuContextByteRange(actual)
	return manuContextRangeMatchScore(expectedStart, expectedEnd, actualStart, actualEnd)
}

func manuContextRangeMatchScore[T int | int64](expectedStart, expectedEnd, actualStart, actualEnd T) int {
	if expectedStart == actualStart && expectedEnd == actualEnd {
		return 40
	}
	if (expectedStart <= actualStart && actualEnd <= expectedEnd) ||
		(actualStart <= expectedStart && expectedEnd <= actualEnd) {
		return 30
	}
	return 20
}

func manuContextPathMatches(expected, actual string) bool {
	if expected == "" {
		return actual == ""
	}
	if actual == "" {
		return false
	}
	normalize := func(value string) string {
		return path.Clean(strings.ReplaceAll(value, "\\", "/"))
	}
	normalizedExpected := normalize(expected)
	normalizedActual := normalize(actual)
	if normalizedExpected == normalizedActual {
		return true
	}
	// A basename equivalence is accepted only when one side is already a
	// basename. This avoids equating unrelated directories with same names.
	if !strings.Contains(normalizedExpected, "/") || !strings.Contains(normalizedActual, "/") {
		return path.Base(normalizedExpected) == path.Base(normalizedActual)
	}
	return false
}

func manuContextLocatorDimensionMatches(expected, actual string) bool {
	if expected == "" || actual == "" {
		return true
	}
	return strings.TrimSpace(expected) == strings.TrimSpace(actual)
}

func manuContextHasMeaningfulDimension(locator contract.Locator) bool {
	return strings.TrimSpace(locator.Member) != "" ||
		manuContextHasLinePosition(locator) ||
		manuContextHasColumnPosition(locator) ||
		manuContextHasBytePosition(locator)
}

func manuContextHasLinePosition(locator contract.Locator) bool {
	return locator.StartLine > 0 || locator.EndLine > 0
}

func manuContextHasColumnPosition(locator contract.Locator) bool {
	return locator.StartColumn > 0 || locator.EndColumn > 0
}

func manuContextHasBytePosition(locator contract.Locator) bool {
	return locator.ByteOffset > 0 || locator.ByteLength > 0
}

func manuContextLineRangesOverlap(expected, actual contract.Locator) bool {
	expectedStart, expectedEnd := manuContextLineRange(expected)
	actualStart, actualEnd := manuContextLineRange(actual)
	return expectedStart <= actualEnd && actualStart <= expectedEnd
}

func manuContextLineRange(locator contract.Locator) (int, int) {
	start, end := locator.StartLine, locator.EndLine
	if start == 0 {
		start = 1
	}
	if end == 0 {
		end = start
	}
	return start, end
}

func manuContextColumnRangesOverlap(expected, actual contract.Locator) bool {
	expectedStart, expectedEnd := manuContextColumnRange(expected)
	actualStart, actualEnd := manuContextColumnRange(actual)
	return expectedStart <= actualEnd && actualStart <= expectedEnd
}

func manuContextColumnRange(locator contract.Locator) (int, int) {
	start, end := locator.StartColumn, locator.EndColumn
	if start == 0 {
		start = end
	}
	if end == 0 {
		end = start
	}
	return start, end
}

func manuContextByteRangesOverlap(expected, actual contract.Locator) bool {
	expectedStart, expectedEnd := manuContextByteRange(expected)
	actualStart, actualEnd := manuContextByteRange(actual)
	return expectedStart <= actualEnd && actualStart <= expectedEnd
}

func manuContextByteRange(locator contract.Locator) (int64, int64) {
	start := locator.ByteOffset
	if locator.ByteLength == 0 {
		return start, start
	}
	if locator.ByteLength-1 > math.MaxInt64-start {
		return start, math.MaxInt64
	}
	return start, start + locator.ByteLength - 1
}

func manuContextUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:0]
	for _, value := range values {
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

type manuContextProjection struct {
	Version         string                          `json:"version"`
	CaseID          string                          `json:"case_id"`
	CaseVersion     int                             `json:"case_version"`
	SourceID        string                          `json:"source_id"`
	SourceRevision  string                          `json:"source_revision"`
	Scope           query.Scope                     `json:"scope"`
	PackageID       string                          `json:"package_id"`
	PackageDigest   string                          `json:"package_digest"`
	PackageRevision string                          `json:"package_revision"`
	IntentKind      query.IntentKind                `json:"intent_kind"`
	Items           []manuContextProjectionItem     `json:"items"`
	Relations       []manuContextProjectionRelation `json:"relations"`
	EvidenceIDs     []string                        `json:"evidence_ids"`
	Degradations    []query.ContextDegradation      `json:"degradations"`
	Truncated       bool                            `json:"truncated"`
	TokenEstimate   int                             `json:"token_estimate"`
	CharactersUsed  int64                           `json:"characters_used"`
	BytesUsed       int64                           `json:"bytes_used"`
}

type manuContextProjectionItem struct {
	ID         string                       `json:"id"`
	Kind       query.ContextItemKind        `json:"kind"`
	Origin     query.ContextKnowledgeKind   `json:"origin"`
	Locator    manuContextProjectionLocator `json:"locator"`
	SupportIDs []string                     `json:"support_ids"`
}

type manuContextProjectionRelation struct {
	ID         string                     `json:"id"`
	Predicate  string                     `json:"predicate"`
	Origin     query.ContextKnowledgeKind `json:"origin"`
	FromID     string                     `json:"from_id"`
	ToID       string                     `json:"to_id"`
	Path       []string                   `json:"path"`
	SupportIDs []string                   `json:"support_ids"`
}

type manuContextProjectionLocator struct {
	Path        string `json:"path,omitempty"`
	Member      string `json:"member,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	StartColumn int    `json:"start_column,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	EndColumn   int    `json:"end_column,omitempty"`
	ByteOffset  int64  `json:"byte_offset,omitempty"`
	ByteLength  int64  `json:"byte_length,omitempty"`
}

func manuContextSafeProjection(
	request VariantExecutionRequest,
	packageContext query.ContextPackage,
	matched []string,
) (manuContextProjection, []byte, error) {
	projection := manuContextProjection{
		Version:         VariantExecutionVersion,
		CaseID:          request.Case.CaseID,
		CaseVersion:     request.Case.CaseVersion,
		SourceID:        request.SourceID,
		SourceRevision:  request.SourceRevision,
		Scope:           packageContext.Scope,
		PackageID:       packageContext.ID,
		PackageDigest:   packageContext.Digest,
		PackageRevision: packageContext.Revision,
		IntentKind:      packageContext.Intent.Kind,
		Items:           make([]manuContextProjectionItem, 0, len(packageContext.Items)),
		Relations:       make([]manuContextProjectionRelation, 0, len(packageContext.Relations)),
		EvidenceIDs:     append([]string(nil), matched...),
		Degradations:    append([]query.ContextDegradation(nil), packageContext.Degradations...),
		Truncated:       packageContext.Truncated,
		TokenEstimate:   packageContext.TokenEstimate,
		CharactersUsed:  packageContext.CharactersUsed,
		BytesUsed:       packageContext.BytesUsed,
	}
	for _, item := range packageContext.Items {
		locator := manuContextItemLocator(item)
		var projectedLocator manuContextProjectionLocator
		if locator != nil {
			projectedLocator = manuContextProjectionLocatorFrom(*locator)
		}
		projection.Items = append(projection.Items, manuContextProjectionItem{
			ID: item.ID, Kind: item.Kind, Origin: item.Origin,
			Locator:    projectedLocator,
			SupportIDs: sortedManuContextStrings(item.SupportIDs),
		})
	}
	for _, relation := range packageContext.Relations {
		projection.Relations = append(projection.Relations, manuContextProjectionRelation{
			ID: relation.ID, Predicate: string(relation.Predicate), Origin: relation.Origin,
			FromID: relation.FromID, ToID: relation.ToID,
			Path:       sortedManuContextStrings(relation.Path),
			SupportIDs: sortedManuContextStrings(relation.SupportIDs),
		})
	}
	sort.SliceStable(projection.Items, func(left, right int) bool { return projection.Items[left].ID < projection.Items[right].ID })
	sort.SliceStable(projection.Relations, func(left, right int) bool { return projection.Relations[left].ID < projection.Relations[right].ID })
	encoded, err := json.Marshal(projection)
	if err != nil {
		return manuContextProjection{}, nil, err
	}
	return projection, encoded, nil
}

func manuContextProjectionLocatorFrom(locator contract.Locator) manuContextProjectionLocator {
	return manuContextProjectionLocator{
		Path: locator.Path, Member: locator.Member,
		StartLine: locator.StartLine, StartColumn: locator.StartColumn,
		EndLine: locator.EndLine, EndColumn: locator.EndColumn,
		ByteOffset: locator.ByteOffset, ByteLength: locator.ByteLength,
	}
}

func sortedManuContextStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return manuContextUniqueStrings(result)
}

func manuContextDuration(duration time.Duration) *VariantDuration {
	if duration < 0 {
		duration = 0
	}
	return &VariantDuration{Value: duration.Nanoseconds(), Unit: VariantDurationNanoseconds}
}
