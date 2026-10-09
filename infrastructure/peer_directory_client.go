package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

var (
	ErrInvalidPeerDirectoryClient = errors.New("invalid Plugin SDK peer-directory client")
	ErrPeerDirectoryUnavailable   = errors.New("plugin SDK Core peer-directory unavailable")
)

type PeerDirectoryPollResult struct {
	Directory   models.PeerDirectory
	ETag        string
	NotModified bool
}

// PeerDirectoryClient polls one authenticated Core directory endpoint for one
// immutable replica identity. It never changes carrier or retries a poll.
type PeerDirectoryClient struct {
	contract    PeerDirectoryPollContract
	baseURL     *url.URL
	transport   *MutualTLSClient
	identity    models.PeerReplicaID
	identityURI string
}

func NewPeerDirectoryClient(contract PeerDirectoryPollContract, baseURL string, transport *MutualTLSClient, identity models.PeerReplicaID) (*PeerDirectoryClient, error) {
	if contract.Validate() != nil || transport == nil || !identity.Valid() {
		return nil, ErrInvalidPeerDirectoryClient
	}
	parsed, ok := parseControlURL(baseURL)
	if !ok {
		return nil, ErrInvalidPeerDirectoryClient
	}
	identityURI, err := peerDirectoryIdentityURI(contract.Identity.URITemplate, identity)
	if err != nil || !transport.PresentsClientCertificateURI(identityURI) {
		return nil, models.ErrReplicaIdentityMismatch
	}
	return &PeerDirectoryClient{
		contract: contract, baseURL: parsed, transport: transport,
		identity: identity, identityURI: identityURI,
	}, nil
}

// Poll performs one conditional read. An empty ETag is the initial immediate
// snapshot request and therefore requires waitMs=0. A non-empty ETag enables
// one bounded long-poll; 304 means unchanged, not failure.
func (client *PeerDirectoryClient) Poll(ctx context.Context, etag string, waitMs int) (PeerDirectoryPollResult, error) {
	if client == nil || ctx == nil || waitMs < 0 || waitMs > client.contract.Poll.MaximumWaitMs ||
		etag == "" && waitMs != 0 || etag != "" && !validPeerDirectoryETag(etag) || !client.identityBound() {
		return PeerDirectoryPollResult{}, ErrInvalidPeerDirectoryClient
	}
	target, err := client.contract.endpointURL(client.baseURL)
	if err != nil {
		return PeerDirectoryPollResult{}, ErrInvalidPeerDirectoryClient
	}
	query := target.Query()
	query.Set(client.contract.Poll.WaitParameter, fmt.Sprintf("%d", waitMs))
	target.RawQuery = query.Encode()
	callContext, cancel := context.WithTimeout(ctx, time.Duration(client.contract.Poll.RequestDeadlineMs)*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(callContext, client.contract.Endpoint.Method, target.String(), nil)
	if err != nil {
		return PeerDirectoryPollResult{}, ErrPeerDirectoryUnavailable
	}
	request.Header.Set("accept", client.contract.DirectoryMediaType)
	if etag != "" {
		request.Header.Set("if-none-match", etag)
	}
	response, err := client.transport.Do(request)
	if err != nil || response == nil || response.Body == nil {
		return PeerDirectoryPollResult{}, ErrPeerDirectoryUnavailable
	}
	defer closeResource(response.Body)
	cacheControl, cacheControlDefined := contractHeaderValue(client.contract.Headers.Response, "cache-control")
	if !cacheControlDefined || !strings.EqualFold(strings.TrimSpace(response.Header.Get("cache-control")), cacheControl) {
		return PeerDirectoryPollResult{}, models.ErrInvalidPeerDirectory
	}
	switch response.StatusCode {
	case client.contract.Responses.Unchanged.Status:
		if etag == "" || response.Header.Get("etag") != etag {
			return PeerDirectoryPollResult{}, models.ErrInvalidPeerDirectory
		}
		body, readErr := readControlBody(response.Body, 1)
		if readErr != nil || len(body) != 0 {
			return PeerDirectoryPollResult{}, models.ErrInvalidPeerDirectory
		}
		return PeerDirectoryPollResult{ETag: etag, NotModified: true}, nil
	case client.contract.Responses.Directory.Status:
		if !responseHasMediaType(response, client.contract.Responses.Directory.MediaType) {
			return PeerDirectoryPollResult{}, models.ErrInvalidPeerDirectory
		}
		body, readErr := readControlBody(response.Body, client.contract.Responses.Directory.MaximumBytes)
		if readErr != nil {
			return PeerDirectoryPollResult{}, models.ErrInvalidPeerDirectory
		}
		directory, parseErr := models.ParsePeerDirectory(body)
		if parseErr != nil || directory.Caller != client.identity {
			return PeerDirectoryPollResult{}, models.ErrInvalidPeerDirectory
		}
		expectedETag := peerDirectoryETag(directory.Generation)
		if response.Header.Get("etag") != expectedETag {
			return PeerDirectoryPollResult{}, models.ErrInvalidPeerDirectory
		}
		return PeerDirectoryPollResult{Directory: directory, ETag: expectedETag}, nil
	default:
		return PeerDirectoryPollResult{}, ErrPeerDirectoryUnavailable
	}
}

func (client *PeerDirectoryClient) identityBound() bool {
	return client != nil && client.transport != nil && client.transport.PresentsClientCertificateURI(client.identityURI)
}

func peerDirectoryETag(generation string) string { return `"` + generation + `"` }

func validPeerDirectoryETag(value string) bool {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	generation := value[1 : len(value)-1]
	return models.ValidGeneration(generation) && peerDirectoryETag(generation) == value
}

func peerDirectoryIdentityURI(template string, identity models.PeerReplicaID) (string, error) {
	if !identity.Valid() || !strings.Contains(template, "{instanceId}") || !strings.Contains(template, "{replicaId}") || !strings.Contains(template, "{incarnationId}") {
		return "", ErrInvalidPeerDirectoryPollContract
	}
	uri := strings.NewReplacer(
		"{instanceId}", url.PathEscape(identity.InstanceID),
		"{replicaId}", url.PathEscape(identity.ReplicaID),
		"{incarnationId}", url.PathEscape(identity.IncarnationID),
	).Replace(template)
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "spiffe" || parsed.Host == "" || strings.Contains(uri, "{") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidPeerDirectoryPollContract
	}
	return uri, nil
}
