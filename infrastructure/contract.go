package infrastructure

import (
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// httpContractAsset is the single source of truth for endpoint paths, limits,
// response codes, outcomes, deadlines, identity and observability names. No
// production package may hardcode a second copy of any of these values.
//
//go:embed assets/plugin-sdk/v1/http-contract.json
var httpContractAsset []byte

var (
	ErrInvalidHTTPContract = errors.New("invalid Plugin SDK HTTP contract")
	ErrUnknownOutcome      = errors.New("unknown Plugin SDK contract outcome")
	ErrContractMismatch    = errors.New("mismatched Plugin SDK contract version")
)

const expectedContractVersion = "liapoldus.plugin-sdk.http.v1"

type HTTPContract struct {
	ContractVersion   string                     `json:"contractVersion"`
	TransportSecurity TransportSecurity          `json:"transportSecurity"`
	Identity          IdentityContract           `json:"identity"`
	Plugin            PluginContract             `json:"plugin"`
	Core              CoreContract               `json:"core"`
	Outcomes          OutcomesContract           `json:"outcomes"`
	Problems          map[string]ProblemContract `json:"problems"`
	OutcomeProblems   map[string]string          `json:"outcomeProblems"`
	SuccessOutcomes   []string                   `json:"successOutcomes"`
	Errors            map[string]ProblemContract `json:"errors"`
	Deadlines         DeadlinesContract          `json:"deadlines"`
	Logging           LoggingContract            `json:"logging"`
	Idempotency       IdempotencyContract        `json:"idempotency"`
}

type TransportSecurity struct {
	MinimumTLSVersion         uint16               `json:"minimumTLSVersion"`
	ClientCertificateRequired bool                 `json:"clientCertificateRequired"`
	TrustDomain               string               `json:"trustDomain"`
	PeerIdentity              PeerIdentityContract `json:"peerIdentity"`
}

type PeerIdentityContract struct {
	CommonNameRequired              bool   `json:"commonNameRequired"`
	CommonNameMaximumLength         int    `json:"commonNameMaximumLength"`
	UniformResourceIdentifierPrefix string `json:"uniformResourceIdentifierPrefix"`
	RevocationFailClosed            bool   `json:"revocationFailClosed"`
}

type IdentityContract struct {
	Replica      ReplicaContract      `json:"replica"`
	Registration RegistrationContract `json:"registration"`
}

type ReplicaContract struct {
	MaximumBytes int      `json:"maximumBytes"`
	Fields       []string `json:"fields"`
}

type RegistrationContract struct {
	MediaType    string   `json:"mediaType"`
	MaximumBytes int64    `json:"maximumBytes"`
	Required     []string `json:"required"`
}

type PluginContract struct {
	Responses             ResponseContract    `json:"responses"`
	Endpoints             map[string]Endpoint `json:"endpoints"`
	ReloadRequest         DocumentContract    `json:"reloadRequest"`
	ReloadAcknowledgement DocumentContract    `json:"reloadAcknowledgement"`
	Readiness             DocumentContract    `json:"readiness"`
	Manifest              DocumentContract    `json:"manifest"`
	ConfigurationSchema   DocumentContract    `json:"configurationSchema"`
	MaximumMetadataBytes  int64               `json:"maximumMetadataBytes"`
}

type DocumentContract struct {
	MediaType    string   `json:"mediaType"`
	MaximumBytes int64    `json:"maximumBytes"`
	Required     []string `json:"required"`
}

type ResponseContract struct {
	Health       HealthContract       `json:"health"`
	ContentTypes ContentTypesContract `json:"contentTypes"`
	Metrics      MetricsContract      `json:"metrics"`
}

type HealthContract struct {
	Status int               `json:"status"`
	Body   map[string]string `json:"body"`
}

type ContentTypesContract struct {
	JSON    string `json:"json"`
	Metrics string `json:"metrics"`
}

type MetricsContract struct {
	ReadyMetricName              string `json:"readyMetricName"`
	ReadyMetricHelp              string `json:"readyMetricHelp"`
	LifecycleCounterName         string `json:"lifecycleCounterName"`
	LifecycleCounterHelp         string `json:"lifecycleCounterHelp"`
	LifecycleCounterKindLabel    string `json:"lifecycleCounterKindLabel"`
	LifecycleCounterOutcomeLabel string `json:"lifecycleCounterOutcomeLabel"`
	PullFailureCounterName       string `json:"pullFailureCounterName"`
	PullFailureCounterHelp       string `json:"pullFailureCounterHelp"`
}

type CoreContract struct {
	ConfigPull  PullContract   `json:"configPull"`
	SecretGrant SecretContract `json:"secretGrant"`
}

type PullContract struct {
	Method            string            `json:"method"`
	PathTemplate      string            `json:"pathTemplate"`
	ResponseMediaType string            `json:"responseMediaType"`
	MaximumBytes      int64             `json:"maximumBytes"`
	RequestMediaType  string            `json:"requestMediaType"`
	GenerationStates  []string          `json:"generationStates"`
	ResponseHeaders   map[string]string `json:"responseHeaders"`
}

type SecretContract struct {
	Issue                  SecretEndpointContract `json:"issue"`
	Redemption             SecretEndpointContract `json:"redemption"`
	HandleMaximumLength    int                    `json:"handleMaximumLength"`
	ReferenceMaximumLength int                    `json:"referenceMaximumLength"`
	PurposeMaximumLength   int                    `json:"purposeMaximumLength"`
	RedemptionUseLimit     int                    `json:"redemptionUseLimit"`
}

type SecretEndpointContract struct {
	Method               string   `json:"method"`
	PathTemplate         string   `json:"pathTemplate"`
	RequestMediaType     string   `json:"requestMediaType"`
	ResponseMediaType    string   `json:"responseMediaType"`
	MaximumRequestBytes  int64    `json:"maximumRequestBytes"`
	MaximumResponseBytes int64    `json:"maximumResponseBytes"`
	Required             []string `json:"required"`
}

type OutcomesContract struct {
	Reload           []string `json:"reload"`
	ConfigPull       []string `json:"configPull"`
	SecretRedemption []string `json:"secretRedemption"`
}

type ProblemContract struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
}

type DeadlinesContract struct {
	PluginReloadSeconds         int `json:"pluginReloadSeconds"`
	PluginReadinessSeconds      int `json:"pluginReadinessSeconds"`
	CoreConfigPullSeconds       int `json:"coreConfigPullSeconds"`
	CoreSecretGrantSeconds      int `json:"coreSecretGrantSeconds"`
	CoreSecretRedemptionSeconds int `json:"coreSecretRedemptionSeconds"`
	PluginReadHeaderSeconds     int `json:"pluginReadHeaderSeconds"`
	PluginReadSeconds           int `json:"pluginReadSeconds"`
	PluginWriteSeconds          int `json:"pluginWriteSeconds"`
	PluginIdleSeconds           int `json:"pluginIdleSeconds"`
	PluginShutdownGraceSeconds  int `json:"pluginShutdownGraceSeconds"`
	ClientResponseHeaderSeconds int `json:"clientResponseHeaderSeconds"`
	ClientDialSeconds           int `json:"clientDialSeconds"`
}

type LoggingContract struct {
	Stream              string   `json:"stream"`
	Levels              []string `json:"levels"`
	DefaultLevel        string   `json:"defaultLevel"`
	MaximumFields       int      `json:"maximumFields"`
	MaximumKeyLength    int      `json:"maximumKeyLength"`
	MaximumValueLength  int      `json:"maximumValueLength"`
	RedactedPlaceholder string   `json:"redactedPlaceholder"`
	RedactedKeys        []string `json:"redactedKeys"`
	AlwaysRedactedKeys  []string `json:"alwaysRedactedKeys"`
}

type IdempotencyContract struct {
	ReloadKeyedBy                            []string `json:"reloadKeyedBy"`
	RepeatOfActiveGeneration                 string   `json:"repeatOfActiveGeneration"`
	ConflictingDescriptorForActiveGeneration string   `json:"conflictingDescriptorForActiveGeneration"`
}

type Endpoint struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Endpoint returns the contract endpoint registered under a stable logical name.
// Every handler and client route must resolve its path through this accessor so
// the embedded asset stays the only place a route string exists.
func (contract HTTPContract) Endpoint(name string) (Endpoint, error) {
	endpoint, ok := contract.Plugin.Endpoints[name]
	if !ok || endpoint.Method == "" || !strings.HasPrefix(endpoint.Path, "/") {
		return Endpoint{}, fmt.Errorf("%w: endpoint %q", ErrInvalidHTTPContract, name)
	}
	return endpoint, nil
}

// EndpointNames lists every registered endpoint name in stable order.
func (contract HTTPContract) EndpointNames() []string {
	names := make([]string, 0, len(contract.Plugin.Endpoints))
	for name := range contract.Plugin.Endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Problem returns the public-safe status and code for one contract error key.
func (contract HTTPContract) Problem(key string) (ProblemContract, error) {
	problem, ok := contract.Errors[key]
	if !ok || problem.Status < 100 || problem.Status > 599 || problem.Code == "" {
		return ProblemContract{}, fmt.Errorf("%w: error %q", ErrInvalidHTTPContract, key)
	}
	return problem, nil
}

// IsSuccessOutcome reports whether an outcome acknowledges success. The
// vocabulary is closed in the asset, so no caller-supplied string can invent a
// new success classification.
func (contract HTTPContract) IsSuccessOutcome(outcome string) bool {
	return contractContains(contract.SuccessOutcomes, outcome)
}

// StatusForOutcome maps a bounded outcome to the contract problem that owns it.
// The outcome-to-problem mapping lives in the asset, so a Go constant can never
// drift away from the published status code.
func (contract HTTPContract) StatusForOutcome(outcome string) (ProblemContract, error) {
	if contract.IsSuccessOutcome(outcome) {
		return ProblemContract{}, fmt.Errorf("%w: success outcome %q", ErrUnknownOutcome, outcome)
	}
	key, ok := contract.OutcomeProblems[outcome]
	if !ok {
		return ProblemContract{}, fmt.Errorf("%w: %q", ErrUnknownOutcome, outcome)
	}
	return contract.Problem(key)
}

func contractContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// ControlURL expands a Core path template with one already-validated opaque
// segment. The segment is percent-encoded, so a generation can never escape its
// path position or inject a query, fragment or extra segment.
func (contract HTTPContract) ControlURL(base *url.URL, pathTemplate, segment, segmentName string) (*url.URL, error) {
	if base == nil || base.Scheme != "https" || base.Host == "" || base.User != nil ||
		base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("%w: control base URL", ErrInvalidHTTPContract)
	}
	if !strings.HasPrefix(pathTemplate, "/") {
		return nil, fmt.Errorf("%w: control path template", ErrInvalidHTTPContract)
	}
	placeholder := "{" + segmentName + "}"
	if !strings.Contains(pathTemplate, placeholder) {
		return nil, fmt.Errorf("%w: control path template placeholder", ErrInvalidHTTPContract)
	}
	expanded := strings.Replace(pathTemplate, placeholder, url.PathEscape(segment), 1)
	resolved, err := url.ParseRequestURI(expanded)
	if err != nil || resolved.Host != "" || resolved.Scheme != "" {
		return nil, fmt.Errorf("%w: control path expansion", ErrInvalidHTTPContract)
	}
	target := *base
	target.Path = strings.TrimSuffix(base.Path, "/") + resolved.Path
	target.RawPath = ""
	return &target, nil
}

// LoadHTTPContract parses and validates the embedded versioned contract asset.
func LoadHTTPContract() (HTTPContract, error) {
	var contract HTTPContract
	decoder := json.NewDecoder(strings.NewReader(string(httpContractAsset)))
	if err := decoder.Decode(&contract); err != nil {
		return HTTPContract{}, ErrInvalidHTTPContract
	}
	if err := contract.validate(); err != nil {
		return HTTPContract{}, err
	}
	return contract, nil
}

func (contract HTTPContract) validate() error {
	switch {
	case contract.ContractVersion != expectedContractVersion:
		return fmt.Errorf("%w: version %q", ErrContractMismatch, contract.ContractVersion)
	case contract.TransportSecurity.MinimumTLSVersion < tls.VersionTLS13:
		return fmt.Errorf("%w: minimum TLS version", ErrInvalidHTTPContract)
	case !contract.TransportSecurity.ClientCertificateRequired:
		return fmt.Errorf("%w: client certificate requirement", ErrInvalidHTTPContract)
	case !contract.TransportSecurity.PeerIdentity.RevocationFailClosed:
		return fmt.Errorf("%w: revocation policy", ErrInvalidHTTPContract)
	case len(contract.Plugin.Endpoints) == 0:
		return fmt.Errorf("%w: endpoints", ErrInvalidHTTPContract)
	case contract.Plugin.MaximumMetadataBytes <= 0,
		contract.Plugin.ReloadRequest.MaximumBytes <= 0,
		contract.Plugin.ReloadAcknowledgement.MaximumBytes <= 0,
		contract.Plugin.Readiness.MaximumBytes <= 0,
		contract.Plugin.Manifest.MaximumBytes <= 0,
		contract.Plugin.ConfigurationSchema.MaximumBytes <= 0:
		return fmt.Errorf("%w: document limits", ErrInvalidHTTPContract)
	case contract.Core.ConfigPull.MaximumBytes <= 0,
		contract.Core.ConfigPull.RequestMediaType == "",
		len(contract.Core.ConfigPull.GenerationStates) == 0,
		len(contract.Core.ConfigPull.ResponseHeaders) != 4:
		return fmt.Errorf("%w: core pull contract", ErrInvalidHTTPContract)
	case contract.Core.SecretGrant.HandleMaximumLength <= 0,
		contract.Core.SecretGrant.RedemptionUseLimit != 1,
		contract.Core.SecretGrant.Issue.PathTemplate == "",
		contract.Core.SecretGrant.Redemption.PathTemplate == "":
		return fmt.Errorf("%w: core secret grant contract", ErrInvalidHTTPContract)
	case contract.Logging.MaximumFields <= 0, contract.Logging.MaximumValueLength <= 0,
		contract.Logging.RedactedPlaceholder == "", len(contract.Logging.AlwaysRedactedKeys) == 0:
		return fmt.Errorf("%w: logging contract", ErrInvalidHTTPContract)
	case len(contract.Idempotency.ReloadKeyedBy) == 0,
		contract.Idempotency.RepeatOfActiveGeneration == "",
		contract.Idempotency.ConflictingDescriptorForActiveGeneration == "":
		return fmt.Errorf("%w: idempotency contract", ErrInvalidHTTPContract)
	case contract.Deadlines.PluginReloadSeconds <= 0, contract.Deadlines.CoreConfigPullSeconds <= 0,
		contract.Deadlines.ClientDialSeconds <= 0, contract.Deadlines.PluginWriteSeconds <= 0:
		return fmt.Errorf("%w: deadlines", ErrInvalidHTTPContract)
	}
	for name := range contract.Plugin.Endpoints {
		if _, err := contract.Endpoint(name); err != nil {
			return err
		}
	}
	for key := range contract.Errors {
		if _, err := contract.Problem(key); err != nil {
			return err
		}
	}
	for _, outcome := range append(append([]string{}, contract.Outcomes.Reload...),
		append(contract.Outcomes.ConfigPull, contract.Outcomes.SecretRedemption...)...) {
		if outcome == "" {
			return fmt.Errorf("%w: empty outcome", ErrInvalidHTTPContract)
		}
	}
	return nil
}
