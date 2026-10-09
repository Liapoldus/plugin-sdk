package infrastructure

import (
	"errors"
	"net/url"
	"strings"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

var ErrInvalidPeerDirectoryPollContract = errors.New("invalid Plugin SDK peer-directory poll contract")

type PeerDirectoryPollContract struct {
	ContractVersion    string `json:"contractVersion"`
	ProblemMediaType   string `json:"problemMediaType"`
	DirectoryMediaType string `json:"directoryMediaType"`
	TransportSecurity  struct {
		TLSRequired               bool   `json:"tlsRequired"`
		ClientCertificateRequired bool   `json:"clientCertificateRequired"`
		CertificateIdentity       string `json:"certificateIdentity"`
	} `json:"transportSecurity"`
	IdentityBinding string `json:"identityBinding"`
	Identity        struct {
		URITemplate    string `json:"uriTemplate"`
		CommonNameUsed bool   `json:"commonNameUsed"`
	} `json:"identity"`
	Endpoint struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"endpoint"`
	Headers struct {
		Request  []string `json:"request"`
		Response []string `json:"response"`
	} `json:"headers"`
	Poll struct {
		InitialRequest    string `json:"initialRequest"`
		Unchanged         string `json:"unchanged"`
		Changed           string `json:"changed"`
		WaitParameter     string `json:"waitParameter"`
		DefaultWaitMs     int    `json:"defaultWaitMs"`
		MaximumWaitMs     int    `json:"maximumWaitMs"`
		RequestDeadlineMs int    `json:"requestDeadlineMs"`
		ETag              string `json:"etag"`
		IfNoneMatch       string `json:"ifNoneMatch"`
		InitialWait       string `json:"initialWait"`
		Cancellation      string `json:"cancellation"`
		Retry             string `json:"retry"`
	} `json:"poll"`
	Requests struct {
		MaximumBytes           int64    `json:"maximumBytes"`
		QueryParameters        []string `json:"queryParameters"`
		UnknownQueryParameters string   `json:"unknownQueryParameters"`
	} `json:"requests"`
	Responses struct {
		Directory struct {
			Status                               int    `json:"status"`
			MediaType                            string `json:"mediaType"`
			MaximumBytes                         int64  `json:"maximumBytes"`
			Schema                               string `json:"schema"`
			ETagMatches                          string `json:"etagMatches"`
			CallerMustMatchAuthenticatedIdentity bool   `json:"callerMustMatchAuthenticatedIdentity"`
		} `json:"directory"`
		Unchanged struct {
			Status       int    `json:"status"`
			MaximumBytes int64  `json:"maximumBytes"`
			MediaType    string `json:"mediaType"`
			ETag         string `json:"etag"`
		} `json:"unchanged"`
	} `json:"responses"`
	Problems map[string]ReplicaLifecycleProblem `json:"problems"`
}

// LoadPeerDirectoryPollContract returns the immutable versioned Core polling
// contract used by one authenticated plugin replica.
func LoadPeerDirectoryPollContract() (PeerDirectoryPollContract, error) {
	contract := newPeerDirectoryPollContract()
	if contract.Validate() != nil {
		return PeerDirectoryPollContract{}, ErrInvalidPeerDirectoryPollContract
	}
	return contract, nil
}

func (contract PeerDirectoryPollContract) Validate() error {
	if contract.ContractVersion != "liapoldus.plugin-sdk.peer-directory-poll.v1" ||
		!validContractMediaType(contract.ProblemMediaType) || !validContractMediaType(contract.DirectoryMediaType) ||
		contract.ProblemMediaType != contract.DirectoryMediaType ||
		!contract.TransportSecurity.TLSRequired || !contract.TransportSecurity.ClientCertificateRequired ||
		contract.TransportSecurity.CertificateIdentity == "" || contract.IdentityBinding != "authenticated-client-certificate-uri-san" ||
		contract.Identity.CommonNameUsed || !strings.Contains(contract.Identity.URITemplate, "{instanceId}") ||
		!strings.Contains(contract.Identity.URITemplate, "{replicaId}") || !strings.Contains(contract.Identity.URITemplate, "{incarnationId}") ||
		contract.Endpoint.Method != "GET" || !validControlEndpointPath(contract.Endpoint.Path) ||
		contract.Poll.MaximumWaitMs <= 0 || contract.Poll.DefaultWaitMs != contract.Poll.MaximumWaitMs ||
		contract.Poll.RequestDeadlineMs <= contract.Poll.MaximumWaitMs || contract.Poll.WaitParameter != "waitMs" ||
		contract.Poll.InitialRequest != "immediate-current-directory" || contract.Poll.InitialWait != "must-be-zero" ||
		contract.Poll.Unchanged != "304-not-modified" || contract.Poll.Changed != "200-directory" ||
		contract.Poll.ETag != "strong-generation-tag" || contract.Poll.IfNoneMatch != "exact-current-etag-only" ||
		contract.Poll.Cancellation != "abort-pending-wait" || contract.Poll.Retry != "caller-controlled-no-automatic-replay" ||
		contract.Requests.MaximumBytes != 0 || len(contract.Requests.QueryParameters) != 1 || contract.Requests.QueryParameters[0] != contract.Poll.WaitParameter ||
		contract.Requests.UnknownQueryParameters != "reject" || contract.Responses.Directory.Status != 200 ||
		contract.Responses.Directory.MediaType != contract.DirectoryMediaType || contract.Responses.Directory.MaximumBytes != models.PeerDirectoryMaximumBytes ||
		contract.Responses.Directory.Schema == "" || contract.Responses.Directory.ETagMatches != "quoted-directory-generation" ||
		!contract.Responses.Directory.CallerMustMatchAuthenticatedIdentity || contract.Responses.Unchanged.Status != 304 ||
		contract.Responses.Unchanged.MaximumBytes != 0 || contract.Responses.Unchanged.MediaType != "none" {
		return ErrInvalidPeerDirectoryPollContract
	}
	if !contains(contract.Headers.Request, "accept:"+contract.DirectoryMediaType) ||
		!contains(contract.Headers.Request, "if-none-match:"+contract.Poll.ETag) ||
		!contains(contract.Headers.Response, "etag:"+contract.Poll.ETag) {
		return ErrInvalidPeerDirectoryPollContract
	}
	if _, ok := contractHeaderValue(contract.Headers.Response, "cache-control"); !ok {
		return ErrInvalidPeerDirectoryPollContract
	}
	for _, name := range []string{"invalidRequest", "unauthenticated", "identityMismatch", "unavailable"} {
		problem, ok := contract.Problems[name]
		if !ok || problem.Status < 400 || problem.Code == "" {
			return ErrInvalidPeerDirectoryPollContract
		}
	}
	return nil
}

func contractHeaderValue(headers []string, name string) (string, bool) {
	name += ":"
	value := ""
	for _, header := range headers {
		if strings.HasPrefix(strings.ToLower(header), name) {
			if value != "" || strings.TrimSpace(header[len(name):]) == "" {
				return "", false
			}
			value = strings.TrimSpace(header[len(name):])
		}
	}
	return value, value != ""
}

func validContractMediaType(value string) bool {
	return strings.Count(value, "/") == 1 && !strings.ContainsAny(value, " \t\r\n")
}

func validControlEndpointPath(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "" && parsed.Host == "" && parsed.Path != "" &&
		strings.HasPrefix(parsed.Path, "/") && parsed.RawQuery == "" && parsed.Fragment == "" &&
		!strings.ContainsAny(parsed.Path, "\\\x00\r\n")
}

func (contract PeerDirectoryPollContract) endpointURL(base *url.URL) (*url.URL, error) {
	if err := contract.Validate(); err != nil || base == nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrInvalidPeerDirectoryPollContract
	}
	parsed, err := url.ParseRequestURI(contract.Endpoint.Path)
	if err != nil || parsed.Host != "" || parsed.Scheme != "" {
		return nil, ErrInvalidPeerDirectoryPollContract
	}
	target := *base
	target.Path = strings.TrimSuffix(base.Path, "/") + parsed.Path
	target.RawPath = ""
	return &target, nil
}
