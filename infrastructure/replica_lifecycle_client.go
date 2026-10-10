package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

var (
	ErrInvalidReplicaLifecycleClient = errors.New("invalid Plugin SDK replica lifecycle client")
	ErrReplicaLifecycleUnavailable   = errors.New("plugin SDK Core replica lifecycle unavailable")
)

// ReplicaLifecycleClient is the plugin-to-Core mTLS client for one immutable
// replica identity. It performs one call per method and never retries or
// replays a lifecycle request.
type ReplicaLifecycleClient struct {
	contract    ReplicaLifecycleContract
	baseURL     *url.URL
	transport   *MutualTLSClient
	identity    models.PeerReplicaID
	identityURI string
}

// NewReplicaLifecycleClient binds a plugin-to-Core client to the one replica
// identity in its client certificate. Registration payloads with a different
// instance, replica, incarnation or placement are refused locally.
func NewReplicaLifecycleClient(contract ReplicaLifecycleContract, baseURL string, transport *MutualTLSClient, identity models.PeerReplicaID) (*ReplicaLifecycleClient, error) {
	if contract.Validate() != nil || transport == nil || !identity.Valid() {
		return nil, ErrInvalidReplicaLifecycleClient
	}
	parsed, ok := parseControlURL(baseURL)
	if !ok {
		return nil, ErrInvalidReplicaLifecycleClient
	}
	identityURI, err := replicaIdentityURI(contract, identity)
	if err != nil || !transport.PresentsClientCertificateURI(identityURI) {
		return nil, models.ErrReplicaIdentityMismatch
	}
	return &ReplicaLifecycleClient{
		contract: contract, baseURL: parsed, transport: transport,
		identity: identity, identityURI: identityURI,
	}, nil
}

// Register publishes immutable replica metadata and receives the initial lease.
func (client *ReplicaLifecycleClient) Register(ctx context.Context, request models.ReplicaRegistrationRequest) (models.ReplicaRegistrationResponse, error) {
	if client == nil {
		return models.ReplicaRegistrationResponse{}, ErrInvalidReplicaLifecycleClient
	}
	if request.Validate() != nil || request.ContractVersion != client.contract.ContractVersion ||
		len(request.PeerEndpoints) > client.contract.Limits.MaximumPeerEndpoints ||
		len(request.AdvertisedContracts) > client.contract.Limits.MaximumContractsPerList ||
		len(request.AcceptedContracts) > client.contract.Limits.MaximumContractsPerList {
		return models.ReplicaRegistrationResponse{}, models.ErrInvalidReplicaRegistration
	}
	if request.Identity != client.identity || !client.identityBound() {
		return models.ReplicaRegistrationResponse{}, models.ErrReplicaIdentityMismatch
	}
	var response models.ReplicaRegistrationResponse
	if err := client.call(ctx, "register", request, &response); err != nil {
		return models.ReplicaRegistrationResponse{}, err
	}
	if response.Validate(client.contract.ContractVersion, client.identity) != nil ||
		response.LeaseExpiresAt.Sub(response.ServerTime) > time.Duration(client.contract.Lease.TTLSeconds)*time.Second {
		return models.ReplicaRegistrationResponse{}, models.ErrInvalidReplicaLease
	}
	return response, nil
}

// Renew extends only the current incarnation's lease and publishes readiness
// progress. Endpoint, placement, release and contract metadata cannot change.
func (client *ReplicaLifecycleClient) Renew(ctx context.Context, request models.ReplicaRenewalRequest) (models.ReplicaLease, error) {
	if client == nil {
		return models.ReplicaLease{}, ErrInvalidReplicaLifecycleClient
	}
	if request.Validate() != nil || request.ContractVersion != client.contract.ContractVersion {
		return models.ReplicaLease{}, models.ErrInvalidReplicaRegistration
	}
	if request.Identity != client.identity || !client.identityBound() {
		return models.ReplicaLease{}, models.ErrReplicaIdentityMismatch
	}
	var response models.ReplicaLease
	if err := client.call(ctx, "renew", request, &response); err != nil {
		return models.ReplicaLease{}, err
	}
	if response.Validate(client.contract.ContractVersion, client.identity) != nil ||
		response.LeaseExpiresAt.Sub(response.ServerTime) > time.Duration(client.contract.Lease.TTLSeconds)*time.Second {
		return models.ReplicaLease{}, models.ErrInvalidReplicaLease
	}
	return response, nil
}

// Deregister explicitly withdraws this incarnation. Lease expiry remains the
// fail-closed fallback if the process cannot reach Core during shutdown.
func (client *ReplicaLifecycleClient) Deregister(ctx context.Context, request models.ReplicaDeregistrationRequest) (models.ReplicaDeregistrationResponse, error) {
	if client == nil {
		return models.ReplicaDeregistrationResponse{}, ErrInvalidReplicaLifecycleClient
	}
	if request.Validate() != nil || request.ContractVersion != client.contract.ContractVersion {
		return models.ReplicaDeregistrationResponse{}, models.ErrInvalidReplicaRegistration
	}
	if request.Identity != client.identity || !client.identityBound() {
		return models.ReplicaDeregistrationResponse{}, models.ErrReplicaIdentityMismatch
	}
	var response models.ReplicaDeregistrationResponse
	if err := client.call(ctx, "deregister", request, &response); err != nil {
		return models.ReplicaDeregistrationResponse{}, err
	}
	if response.Validate(client.contract.ContractVersion, client.identity) != nil {
		return models.ReplicaDeregistrationResponse{}, models.ErrInvalidReplicaLease
	}
	return response, nil
}

func (client *ReplicaLifecycleClient) identityBound() bool {
	return client != nil && client.transport != nil && client.transport.PresentsClientCertificateURI(client.identityURI)
}

func (client *ReplicaLifecycleClient) call(ctx context.Context, operation string, input, output any) error {
	requestContract, requestOK := client.contract.Requests[operation]
	responseContract, responseOK := client.contract.Responses[operation]
	endpoint, endpointOK := client.contract.Endpoints[operation]
	if !requestOK || !responseOK || !endpointOK || ctx == nil {
		return ErrInvalidReplicaLifecycleClient
	}
	requestBody, err := json.Marshal(input)
	if err != nil || int64(len(requestBody)) > requestContract.MaximumBytes || !models.ValidJSONObject(requestBody) {
		return models.ErrInvalidReplicaRegistration
	}
	target, err := client.contract.endpointURL(client.baseURL, operation)
	if err != nil {
		return ErrInvalidReplicaLifecycleClient
	}
	deadline := client.contract.Deadlines.RegisterSeconds
	switch operation {
	case "renew":
		deadline = client.contract.Deadlines.RenewSeconds
	case "deregister":
		deadline = client.contract.Deadlines.DeregisterSeconds
	}
	callContext, cancel := context.WithTimeout(ctx, time.Duration(deadline)*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(callContext, endpoint.Method, target.String(), bytes.NewReader(requestBody))
	if err != nil {
		return ErrReplicaLifecycleUnavailable
	}
	httpRequest.Header.Set("content-type", requestContract.MediaType)
	httpRequest.Header.Set("accept", responseContract.MediaType)
	response, err := client.transport.Do(httpRequest)
	if err != nil {
		return ErrReplicaLifecycleUnavailable
	}
	if response == nil || response.Body == nil {
		return ErrReplicaLifecycleUnavailable
	}
	defer closeResource(response.Body)
	if response.StatusCode != responseContract.Status {
		return client.refusal(response)
	}
	if !responseHasMediaType(response, responseContract.MediaType) {
		return models.ErrInvalidReplicaLease
	}
	body, err := readControlBody(response.Body, responseContract.MaximumBytes)
	if err != nil || !models.ValidJSONObject(body) {
		return models.ErrInvalidReplicaLease
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return models.ErrInvalidReplicaLease
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return models.ErrInvalidReplicaLease
	}
	return nil
}

func (client *ReplicaLifecycleClient) refusal(response *http.Response) error {
	if response == nil || response.Body == nil {
		return ErrReplicaLifecycleUnavailable
	}
	if !responseHasMediaType(response, client.contract.ProblemMediaType) {
		return models.ErrReplicaLifecycleRefused
	}
	body, err := readControlBody(response.Body, 4096)
	if err != nil || !models.ValidJSONObject(body) {
		return models.ErrReplicaLifecycleRefused
	}
	var problem struct {
		Code string `json:"code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&problem) != nil {
		return models.ErrReplicaLifecycleRefused
	}
	for name, declared := range client.contract.Problems {
		if declared.Code != problem.Code || declared.Status != response.StatusCode {
			continue
		}
		switch name {
		case "identityMismatch":
			return models.ErrReplicaIdentityMismatch
		case "identityConflict":
			return models.ErrReplicaIdentityConflict
		case "leaseExpired":
			return models.ErrReplicaLeaseExpired
		case "notRegistered":
			return models.ErrReplicaNotRegistered
		case "unavailable":
			return ErrReplicaLifecycleUnavailable
		default:
			return models.ErrReplicaLifecycleRefused
		}
	}
	return models.ErrReplicaLifecycleRefused
}

func responseHasMediaType(response *http.Response, expected string) bool {
	if response == nil {
		return false
	}
	actual := strings.TrimSpace(strings.SplitN(response.Header.Get("content-type"), ";", 2)[0])
	return strings.EqualFold(actual, expected)
}
