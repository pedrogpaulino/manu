package mcpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/query"
)

const contextEvidenceResourceTemplate = "manu://organizations/{organization}/sources/{source}/snapshots/{snapshot}/evidence/{id}"

var (
	// ErrInvalidContextServerOptions reports a configuration that cannot be
	// used to construct a context server.
	ErrInvalidContextServerOptions = errors.New("mcpadapter: invalid context server options")
	// ErrInvalidContextResource is intentionally opaque to avoid enumerating
	// resource identities or parser details at the MCP boundary.
	ErrInvalidContextResource = errors.New("mcpadapter: invalid context resource")
	// ErrContextResourceFailure is the payload-free resource failure exposed to
	// MCP clients. Service and validation details never cross this boundary.
	ErrContextResourceFailure = errors.New("mcpadapter: context resource unavailable")
)

// ActiveSnapshotResolver optionally supplies the active snapshot for a scope.
// Its result is an indication only; it never replaces the requested snapshot.
type ActiveSnapshotResolver interface {
	ResolveActiveSnapshot(context.Context, query.Scope) (query.Scope, error)
}

// ContextServerOptions controls optional context-server behavior. A non-zero
// ResourceLimits value enables the evidence resource template and fixes the
// limits used for every resource read.
type ContextServerOptions struct {
	ActiveSnapshotResolver ActiveSnapshotResolver
	ResourceLimits         query.ContextLimits
}

// Validate checks optional resolver and resource composition boundaries.
func (o ContextServerOptions) Validate() error {
	if o.ActiveSnapshotResolver != nil && nilActiveSnapshotResolver(o.ActiveSnapshotResolver) {
		return ErrInvalidContextServerOptions
	}
	if o.ResourceLimits != (query.ContextLimits{}) {
		if err := o.ResourceLimits.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidContextServerOptions, err)
		}
	}
	return nil
}

func (o ContextServerOptions) resourcesEnabled() bool {
	return o.ResourceLimits != (query.ContextLimits{})
}

type contextResourceOutput struct {
	query.ContextPackage
	LatestSnapshotID string `json:"latest_snapshot_id,omitempty"`
}

func contextEvidenceResourceHandler(service query.ContextService, options ContextServerOptions) mcp.ResourceHandler {
	return func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if request == nil || request.Params == nil {
			return nil, ErrInvalidContextResource
		}
		scope, evidenceID, err := parseContextEvidenceResource(request.Params.URI)
		if err != nil {
			return nil, ErrInvalidContextResource
		}
		contextRequest := query.ContextRequest{
			Version: query.ContextVersion,
			Scope:   scope,
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    query.IntentKindEvidenceInspection,
				Target: query.IntentTarget{
					Kind: query.IntentTargetEvidence,
					ID:   evidenceID,
				},
			},
			Limits: options.ResourceLimits,
		}
		packageContext, err := service.BuildContext(ctx, contextRequest)
		if err != nil {
			return nil, sanitizeContextResourceError(ctx, err)
		}
		if err := validateContextEvidenceResourcePackage(packageContext, scope, evidenceID); err != nil {
			return nil, ErrContextResourceFailure
		}
		resourceOutput := contextResourceOutput{
			ContextPackage:   packageContext,
			LatestSnapshotID: activeSnapshotID(ctx, options.ActiveSnapshotResolver, packageContext.Scope),
		}
		encoded, err := json.Marshal(resourceOutput)
		if err != nil {
			return nil, ErrContextResourceFailure
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      request.Params.URI,
				MIMEType: "application/json",
				Text:     string(encoded),
			}},
		}, nil
	}
}

func sanitizeContextResourceError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return ErrContextResourceFailure
}

func validateContextEvidenceResourcePackage(packageContext query.ContextPackage, scope query.Scope, evidenceID string) error {
	if err := packageContext.Validate(); err != nil {
		return err
	}
	if !sameContextResourceScope(packageContext.Scope, scope) ||
		packageContext.Intent.Kind != query.IntentKindEvidenceInspection ||
		packageContext.Intent.Target.Kind != query.IntentTargetEvidence ||
		packageContext.Intent.Target.ID != evidenceID {
		return ErrInvalidContextResource
	}
	return nil
}

func parseContextEvidenceResource(rawURI string) (query.Scope, string, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil || parsed.Scheme != "manu" || parsed.Host != "organizations" ||
		parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return query.Scope{}, "", ErrInvalidContextResource
	}
	if parsed.EscapedPath() != parsed.Path {
		return query.Scope{}, "", ErrInvalidContextResource
	}
	segments := strings.Split(parsed.Path, "/")
	if len(segments) != 8 || segments[0] != "" || segments[2] != "sources" ||
		segments[4] != "snapshots" || segments[6] != "evidence" {
		return query.Scope{}, "", ErrInvalidContextResource
	}
	if !contextResourceUUID(segments[1]) || !contextResourceUUID(segments[3]) || !contextResourceUUID(segments[5]) {
		return query.Scope{}, "", ErrInvalidContextResource
	}
	if !contextResourceSegment(segments[7]) {
		return query.Scope{}, "", ErrInvalidContextResource
	}
	scope := query.Scope{
		OrganizationID: segments[1],
		SourceID:       segments[3],
		SnapshotID:     segments[5],
	}
	if err := scope.Validate(); err != nil {
		return query.Scope{}, "", ErrInvalidContextResource
	}
	return scope, segments[7], nil
}

var contextResourceUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func contextResourceUUID(value string) bool {
	return contextResourceUUIDPattern.MatchString(value)
}

func contextResourceSegment(value string) bool {
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsSpace(character) || character < 0x20 || character == 0x7f || character == '/' || character == '\\' || character == '%' || character == '?' || character == '#' {
			return false
		}
	}
	return true
}

func contextEvidenceResourceURI(scope query.Scope, evidenceID string) (string, error) {
	if err := scope.Validate(); err != nil || !contextResourceSegment(evidenceID) {
		return "", ErrInvalidContextResource
	}
	return "manu://organizations/" + scope.OrganizationID + "/sources/" + scope.SourceID + "/snapshots/" + scope.SnapshotID + "/evidence/" + evidenceID, nil
}

func contextEvidenceResourceLinks(packageContext query.ContextPackage) ([]mcp.Content, error) {
	var links []mcp.Content
	for _, item := range packageContext.Items {
		if item.Kind != query.ContextItemEvidence {
			continue
		}
		uri, err := contextEvidenceResourceURI(item.Scope, item.ID)
		if err != nil {
			return nil, err
		}
		links = append(links, &mcp.ResourceLink{
			URI:         uri,
			Name:        item.ID,
			MIMEType:    "application/json",
			Description: "Authorized evidence context.",
		})
	}
	return links, nil
}

func activeSnapshotID(ctx context.Context, resolver ActiveSnapshotResolver, historical query.Scope) string {
	if nilActiveSnapshotResolver(resolver) {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	latest, err := resolver.ResolveActiveSnapshot(ctx, historical)
	if err != nil || ctx.Err() != nil {
		return ""
	}
	if latest.Validate() != nil || !strings.EqualFold(latest.OrganizationID, historical.OrganizationID) || !strings.EqualFold(latest.SourceID, historical.SourceID) || strings.EqualFold(latest.SnapshotID, historical.SnapshotID) {
		return ""
	}
	return latest.SnapshotID
}

func sameContextResourceScope(left, right query.Scope) bool {
	return strings.EqualFold(left.OrganizationID, right.OrganizationID) && strings.EqualFold(left.SourceID, right.SourceID) && strings.EqualFold(left.SnapshotID, right.SnapshotID)
}

func nilActiveSnapshotResolver(resolver ActiveSnapshotResolver) bool {
	if resolver == nil {
		return true
	}
	value := reflect.ValueOf(resolver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
