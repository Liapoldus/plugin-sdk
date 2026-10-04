package presentation

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// ErrMissingHandlerDependency is returned by NewHandlerSet when one of the ports
// a handler needs is absent. It names no port, path, address or document.
var ErrMissingHandlerDependency = errors.New("missing Plugin SDK handler dependency")

// ErrIncompatibleMetricsExposition is returned when the metrics collector
// announces a media type other than the one the contract serves, because
// serving it would put a response on the wire that the contract does not
// describe. It carries no value.
var ErrIncompatibleMetricsExposition = errors.New("metrics exposition does not match the Plugin SDK contract media type")

// ArtifactInput is a bounded product-neutral artifact invocation. Metadata and
// media type are supplied by Core after authorization; Body is streamed and
// must be consumed before AcceptArtifact returns.
type ArtifactInput struct {
	Invocation  models.ArtifactInvocation
	Metadata    []byte
	ContentType string
	Body        io.Reader
}

// ArtifactResponse is a product-owned HTTP result. The SDK only bounds and
// validates its JSON body; it does not interpret operation or error fields.
type ArtifactResponse struct {
	StatusCode int
	Body       []byte
}

// ArtifactAcceptor consumes one streamed artifact and returns its product-owned
// result. Implementations must finish consuming Body before returning.
type ArtifactAcceptor interface {
	AcceptArtifact(ctx context.Context, input ArtifactInput) (ArtifactResponse, error)
}

// AdminSurfaceProvider publishes the plugin-owned generic management descriptor
// as exact JSON bytes. The SDK bounds and validates the document but does not
// interpret its schema or product fields.
type AdminSurfaceProvider interface {
	AdminSurface(ctx context.Context) ([]byte, error)
}

// AdminActionInput contains an authorized generic invocation and its exact JSON
// request bytes. Product meaning remains entirely inside the callback.
type AdminActionInput struct {
	Invocation models.AdminActionInvocation
	Body       []byte
}

type AdminActionResponse struct {
	StatusCode int
	Body       []byte
}

type AdminActionHandler interface {
	HandleAdminAction(ctx context.Context, input AdminActionInput) (AdminActionResponse, error)
}

// The ports below are the whole surface the handler layer needs. They are
// deliberately narrow and structural, so the application lifecycle and the
// infrastructure metrics collector satisfy them without this package importing
// either layer or naming a concrete type.

// Lifecycle is the Reload use case of the application layer. It returns a
// non-success acknowledgement carrying the contract outcome for every refusal,
// so this layer never parses an error to learn what happened.
type Lifecycle interface {
	Reload(ctx context.Context, request models.Reload) (models.ReloadAcknowledgement, error)
}

// ReadinessProvider reports the generation this replica has actually applied. It
// takes a context so the handler can bound one read with the contract readiness
// deadline and a provider can abandon a read it cannot complete.
type ReadinessProvider interface {
	Readiness(ctx context.Context) models.Readiness
}

// RegistrationProvider publishes the bootstrap identity document. It receives the
// injected contract version so the document always advertises the contract this
// handler set actually answers for.
type RegistrationProvider interface {
	Registration(contractVersion string) (models.Registration, error)
}

// MetadataProvider produces the two plugin-owned documents this layer publishes
// verbatim: the manifest and the configuration schema. The bytes it returns are
// the exact bytes that are written; this layer never decodes, reorders or
// rewrites them, only checks that they are one JSON object within the contract
// limit.
type MetadataProvider interface {
	Manifest(ctx context.Context) ([]byte, error)
	ConfigurationSchema(ctx context.Context) ([]byte, error)
}

// MetricsExposition is the collector's view of the contract metrics surface. The
// handler writes whatever the collector renders and never re-derives a label or a
// metric name, so the exposition stays owned by the instrumentation layer.
type MetricsExposition interface {
	// ContentType is the media type WriteText produces.
	ContentType() string
	// WriteText renders the current metrics in the contract exposition format.
	WriteText(writer io.Writer) error
}

// HandlerConfiguration is the whole set a plugin author must fill to publish the
// SDK REST surface. It pairs the injected contract with one value per port, and
// holds no listener, address, certificate, key, token or product setting.
type HandlerConfiguration struct {
	// Contracts is the versioned contract, obtained by the composition root from
	// the infrastructure contract loader.
	Contracts Contracts
	// Lifecycle is the application Reload use case.
	Lifecycle Lifecycle
	// Readiness reports the applied generation.
	Readiness ReadinessProvider
	// Registration publishes the identity document.
	Registration RegistrationProvider
	// Metadata produces the manifest and the configuration schema.
	Metadata MetadataProvider
	// Metrics is the instrumentation collector.
	Metrics MetricsExposition
	// Artifacts handles the generic binary-action transport. It is optional for
	// plugins that do not declare artifact actions in their own capabilities.
	Artifacts    ArtifactAcceptor
	AdminSurface AdminSurfaceProvider
	AdminActions AdminActionHandler
}

// HandlerSet is the built REST surface. It owns a request multiplexer whose
// routes, statuses, codes, media types and limits all come from the injected
// contract, and it is safe for concurrent use because it holds no per-request
// state beyond what the request already carries.
type HandlerSet struct {
	contracts    Contracts
	lifecycle    Lifecycle
	readiness    ReadinessProvider
	registration RegistrationProvider
	metadata     MetadataProvider
	metrics      MetricsExposition
	artifacts    ArtifactAcceptor
	adminSurface AdminSurfaceProvider
	adminActions AdminActionHandler
	handler      http.Handler
}

// NewHandlerSet validates the injected contract, requires one value per port and
// builds the request multiplexer. It fails closed: a contract whose routes,
// media types, limits, deadlines, problems or document field lists cannot
// produce a conformant surface is refused here, at composition time, rather than
// answering a wrong status on the first request.
func NewHandlerSet(configuration HandlerConfiguration) (*HandlerSet, error) {
	if err := configuration.Contracts.Validate(); err != nil {
		return nil, err
	}
	if configuration.Lifecycle == nil || configuration.Readiness == nil ||
		configuration.Registration == nil || configuration.Metadata == nil ||
		configuration.Metrics == nil || configuration.AdminSurface == nil || configuration.AdminActions == nil {
		return nil, ErrMissingHandlerDependency
	}
	// The collector announces the media type it renders. If that is not the
	// media type the contract serves, the two pieces of wiring disagree and the
	// only safe outcome is to refuse to build the surface.
	if configuration.Metrics.ContentType() != configuration.Contracts.ContentTypes.Metrics {
		return nil, ErrIncompatibleMetricsExposition
	}
	set := &HandlerSet{
		contracts:    configuration.Contracts,
		lifecycle:    configuration.Lifecycle,
		readiness:    configuration.Readiness,
		registration: configuration.Registration,
		metadata:     configuration.Metadata,
		metrics:      configuration.Metrics,
		artifacts:    configuration.Artifacts,
		adminSurface: configuration.AdminSurface,
		adminActions: configuration.AdminActions,
	}
	set.handler = set.routes()
	return set, nil
}

// Handler returns the request multiplexer. It never serves on its own and never
// listens: the composition root owns the transport that carries mTLS, and this
// layer only answers requests that reached it.
func (set *HandlerSet) Handler() http.Handler {
	return set.handler
}

// routes builds the multiplexer from the injected contract.
//
// Each endpoint is registered on its injected path and the method is checked
// inside the handler, so a known path reached with the wrong method answers the
// contract's methodNotAllowed document instead of the multiplexer's built-in
// reply. A single catch-all answers the contract's notFound document for every
// path the contract does not publish.
func (set *HandlerSet) routes() http.Handler {
	mux := http.NewServeMux()
	serve := func(endpoint Endpoint, handler http.HandlerFunc) {
		mux.HandleFunc(endpoint.Path, func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != endpoint.Method {
				writer.Header().Set("Allow", endpoint.Method)
				set.writeTransportProblem(writer, methodNotAllowedKey)
				return
			}
			handler(writer, request)
		})
	}
	serve(set.contracts.IdentityEndpoint, set.handleIdentity)
	serve(set.contracts.ManifestEndpoint, set.handleManifest)
	serve(set.contracts.ConfigSchemaEndpoint, set.handleConfigurationSchema)
	serve(set.contracts.HealthEndpoint, set.handleHealth)
	serve(set.contracts.ReadyEndpoint, set.handleReady)
	serve(set.contracts.ReloadEndpoint, set.handleReload)
	serve(set.contracts.ArtifactStreamEndpoint, set.handleArtifactStream)
	serve(set.contracts.AdminSurfaceEndpoint, set.handleAdminSurface)
	serve(set.contracts.AdminActionEndpoint, set.handleAdminAction)
	serve(set.contracts.MetricsEndpoint, set.handleMetrics)
	mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
		set.writeTransportProblem(writer, notFoundKey)
	})
	return mux
}

// WithoutContext adapts a synchronous readiness reader to the ReadinessProvider
// port. The application lifecycle reads its own in-memory state without a
// context, so this one-line adapter lets an existing lifecycle be passed straight
// in while the handler still bounds every readiness read with the contract
// deadline. It is provided here so the composition root does not have to repeat
// it for every plugin.
func WithoutContext(read func() models.Readiness) ReadinessProvider {
	return ReadinessProviderFunc(func(context.Context) models.Readiness { return read() })
}

// ReadinessProviderFunc adapts a function to the ReadinessProvider port.
type ReadinessProviderFunc func(ctx context.Context) models.Readiness

// Readiness implements ReadinessProvider.
func (reader ReadinessProviderFunc) Readiness(ctx context.Context) models.Readiness {
	return reader(ctx)
}
