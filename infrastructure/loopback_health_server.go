package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

var ErrInvalidLoopbackPlaintextServer = errors.New("invalid Plugin SDK loopback plaintext health server")

// LoopbackHealthServer is an explicitly selected development-only REST server.
// It has no caller-provided handler: the SDK itself serves exactly the generic
// health endpoint from the versioned contracts and rejects every other path.
// Config, grants and lifecycle operations remain on MutualTLSServer clients.
type LoopbackHealthServer struct {
	server *http.Server
	grace  time.Duration
}

// NewLoopbackHealthServer opts one plugin process into the loopback-only,
// plaintext health profile. The default Plugin SDK composition does not create
// this server; choosing it always requires an explicit call to this constructor.
func NewLoopbackHealthServer(contract HTTPContract, errorLog *log.Logger) (*LoopbackHealthServer, error) {
	if err := contract.validate(); err != nil {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	profile, err := LoadLoopbackPlaintextProfile()
	if err != nil || profile.ContractVersion != expectedLoopbackPlaintextVersion ||
		profile.EnabledByDefault || profile.Transport.Network != "tcp" ||
		!profile.Transport.HostAddressMustBeLiteralIP || !profile.Transport.HostMustBeLoopback ||
		profile.Transport.RemoteBindAllowed || profile.Transport.TLSFailureFallback ||
		profile.ExposedEndpoint.HTTPContractVersion != contract.ContractVersion ||
		profile.ExposedEndpoint.EndpointKey == "" {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	if len(profile.CoreEndpointsAlwaysRequireMutualTLS) == 0 {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	allowedExcluded := make(map[string]struct{}, len(profile.ExcludedEndpointKeys))
	for _, name := range profile.ExcludedEndpointKeys {
		if name == profile.ExposedEndpoint.EndpointKey {
			return nil, ErrInvalidLoopbackPlaintextServer
		}
		allowedExcluded[name] = struct{}{}
	}
	for name := range contract.Plugin.Endpoints {
		if name == profile.ExposedEndpoint.EndpointKey {
			continue
		}
		if _, excluded := allowedExcluded[name]; !excluded {
			return nil, ErrInvalidLoopbackPlaintextServer
		}
	}
	if len(allowedExcluded) != len(contract.Plugin.Endpoints)-1 {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	endpoint, err := contract.Endpoint(profile.ExposedEndpoint.EndpointKey)
	if err != nil || endpoint.Method != http.MethodGet || endpoint.Path == "" {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	notFound, ok := contract.Problems[profile.NotFoundProblemKey]
	if !ok || notFound.Status < 100 || notFound.Status > 599 || notFound.Code == "" {
		return nil, ErrInvalidLoopbackPlaintextServer
	}

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != endpoint.Method || request.URL.Path != endpoint.Path {
			writeLoopbackProblem(writer, contract.Plugin.Responses.ContentTypes.JSON, notFound, profile.ProblemResponseField)
			return
		}
		writer.Header().Set("Content-Type", contract.Plugin.Responses.ContentTypes.JSON)
		writer.WriteHeader(contract.Plugin.Responses.Health.Status)
		if err := json.NewEncoder(writer).Encode(contract.Plugin.Responses.Health.Body); err != nil {
			panic(http.ErrAbortHandler)
		}
	})
	server := &LoopbackHealthServer{
		grace: time.Duration(contract.Deadlines.PluginShutdownGraceSeconds) * time.Second,
	}
	server.server = &http.Server{
		Handler:           handler,
		ErrorLog:          errorLog,
		ReadHeaderTimeout: time.Duration(contract.Deadlines.PluginReadHeaderSeconds) * time.Second,
		ReadTimeout:       time.Duration(contract.Deadlines.PluginReadSeconds) * time.Second,
		WriteTimeout:      time.Duration(contract.Deadlines.PluginWriteSeconds) * time.Second,
		IdleTimeout:       time.Duration(contract.Deadlines.PluginIdleSeconds) * time.Second,
	}
	return server, nil
}

func writeLoopbackProblem(writer http.ResponseWriter, mediaType string, problem ProblemContract, codeField string) {
	writer.Header().Set("Content-Type", mediaType)
	writer.WriteHeader(problem.Status)
	if err := json.NewEncoder(writer).Encode(map[string]string{codeField: problem.Code}); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// Listen opens a TCP listener only for a literal loopback IP address. Hostnames
// are rejected instead of being resolved, preventing DNS from selecting a
// non-loopback interface. The selected listener is checked again after binding.
func (server *LoopbackHealthServer) Listen(address string) (net.Listener, error) {
	if server == nil || server.server == nil {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	parsedIP := net.ParseIP(host)
	port, portErr := strconv.Atoi(portText)
	if parsedIP == nil || !parsedIP.IsLoopback() || portErr != nil || port < 0 || port > 65535 {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(parsedIP.String(), portText))
	if err != nil {
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	if !isLoopbackTCPListener(listener) {
		if err := listener.Close(); err != nil {
			return nil, ErrInvalidLoopbackPlaintextServer
		}
		return nil, ErrInvalidLoopbackPlaintextServer
	}
	return listener, nil
}

// Serve accepts only a TCP listener whose bound address is a concrete loopback
// IP. This protects callers that bind before handing the listener to the SDK.
func (server *LoopbackHealthServer) Serve(listener net.Listener) error {
	if server == nil || server.server == nil || !isLoopbackTCPListener(listener) {
		return ErrInvalidLoopbackPlaintextServer
	}
	return server.server.Serve(listener)
}

// ListenAndServe is the direct composition-root entry point for this profile.
func (server *LoopbackHealthServer) ListenAndServe(address string) error {
	listener, err := server.Listen(address)
	if err != nil {
		return err
	}
	return server.Serve(listener)
}

func isLoopbackTCPListener(listener net.Listener) bool {
	if listener == nil || listener.Addr() == nil || listener.Addr().Network() != "tcp" {
		return false
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	return ok && address.IP != nil && address.IP.IsLoopback() && !address.IP.IsUnspecified() && address.Port > 0
}

// GracefulShutdown drains requests using the same bounded shutdown duration as
// the versioned plugin REST contract.
func (server *LoopbackHealthServer) GracefulShutdown(ctx context.Context) error {
	if server == nil || server.server == nil {
		return ErrInvalidLoopbackPlaintextServer
	}
	deadline := time.Now().Add(server.grace)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := server.server.Shutdown(bounded); err != nil {
		return ErrShutdownGraceExpired
	}
	return nil
}
