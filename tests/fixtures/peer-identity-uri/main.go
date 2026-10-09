package main

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

type result struct {
	URIOnlyIdentityValid           bool `json:"uriOnlyIdentityValid"`
	URIOnlyClientCreated           bool `json:"uriOnlyClientCreated"`
	ExactURIMatches                bool `json:"exactUriMatches"`
	WrongURIRejected               bool `json:"wrongUriRejected"`
	EmptyPresentedIdentityRejected bool `json:"emptyPresentedIdentityRejected"`
	EmptyExpectedIdentityRejected  bool `json:"emptyExpectedIdentityRejected"`
	MalformedURIRejected           bool `json:"malformedUriRejected"`
	WildcardClientRejected         bool `json:"wildcardClientRejected"`
}

type transport struct{}

func (transport) Do(*http.Request) (*http.Response, error) { return nil, nil }

func main() {
	uri := "spiffe://liapoldus/core/replicas/core-a"
	identity, identityErr := models.NewPeerIdentity("", uri)
	contract, contractErr := infrastructure.LoadHTTPContract()
	clientCreated := false
	if identityErr == nil && contractErr == nil {
		_, err := infrastructure.NewPluginClient(contract, "https://plugin.example", transport{}, identity)
		clientCreated = err == nil
	}
	wildcard, wildcardErr := models.NewPeerIdentity("", "spiffe://liapoldus/core/replicas/*")
	_, wildcardClientErr := infrastructure.NewPluginClient(contract, "https://plugin.example", transport{}, wildcard)

	output := result{
		URIOnlyIdentityValid:           identityErr == nil && identity.Valid(),
		URIOnlyClientCreated:           clientCreated,
		ExactURIMatches:                identity.Matches("unregistered-cn", []string{uri}),
		WrongURIRejected:               !identity.Matches("", []string{"spiffe://liapoldus/core/replicas/core-b"}),
		EmptyPresentedIdentityRejected: !identity.Matches("", nil),
		EmptyExpectedIdentityRejected:  func() bool { _, err := models.NewPeerIdentity("", ""); return err != nil }(),
		MalformedURIRejected:           func() bool { _, err := models.NewPeerIdentity("", "not a uri"); return err != nil }(),
		WildcardClientRejected:         wildcardErr == nil && wildcardClientErr != nil,
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		os.Exit(2)
	}
}
