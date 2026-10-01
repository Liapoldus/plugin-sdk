package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

type responseTransport struct{}

func (responseTransport) Do(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       &contextBody{context: request.Context(), reader: strings.NewReader(`{"type":"object"}`)},
	}, nil
}

type contextBody struct {
	context context.Context
	reader  io.Reader
}

func (body *contextBody) Read(buffer []byte) (int, error) {
	if err := body.context.Err(); err != nil {
		return 0, err
	}
	return body.reader.Read(buffer)
}

func (body *contextBody) Close() error { return nil }

func main() {
	contract, err := infrastructure.LoadHTTPContract()
	check(err)
	identity, err := models.NewPeerIdentity("expected-core", "")
	check(err)
	client, err := infrastructure.NewPluginClient(contract, "https://localhost", responseTransport{}, identity)
	check(err)
	contents, err := client.ConfigurationSchema(context.Background())
	check(err)
	check(json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": string(contents)}))
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
