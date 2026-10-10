package infrastructure

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCodeOwnedContractWireParity(t *testing.T) {
	cases := []struct {
		name, path string
		value      any
	}{
		{"http", "assets/plugin-sdk/v2/http-contract.json", newHTTPContract()},
		{"replica", "assets/plugin-sdk/v2/replica-lifecycle.json", newReplicaLifecycleContract()},
		{"poll", "assets/plugin-sdk/v2/peer-directory-poll.json", newPeerDirectoryPollContract()},
		{"loopback", "assets/plugin-sdk/v2/loopback-plaintext-profile.json", newLoopbackPlaintextProfile()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var published, owned any
			if err = json.Unmarshal(raw, &published); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(encoded, &owned); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(published, owned) {
				t.Fatalf("code-owned definition differs from published JSON\npublished: %s\nowned: %s", raw, encoded)
			}
		})
	}
}

func TestContractLoadersReturnOwnedValues(t *testing.T) {
	cases := []struct {
		name string
		load func() (any, error)
	}{
		{"http", func() (any, error) { return LoadHTTPContract() }},
		{"replica", func() (any, error) { return LoadReplicaLifecycleContract() }},
		{"poll", func() (any, error) { return LoadPeerDirectoryPollContract() }},
		{"loopback", func() (any, error) { return LoadLoopbackPlaintextProfile() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := tc.load()
			if err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			mutateContractCollections(reflect.ValueOf(first))
			changed, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, changed) {
				t.Fatal("test did not mutate contract collections")
			}
			next, err := tc.load()
			if err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("caller mutation contaminated subsequent load")
			}
		})
	}
}

func mutateContractCollections(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			mutateContractCollections(v.Field(i))
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			mutateContractCollections(v.MapIndex(key))
			v.SetMapIndex(key, reflect.Value{})
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if v.Index(i).Kind() == reflect.String {
				v.Index(i).SetString("mutated")
			} else {
				mutateContractCollections(v.Index(i))
			}
		}
	}
}

func TestSchemaExportsAreOwnedAndValid(t *testing.T) {
	for _, export := range []func() []byte{PeerDirectorySchema, ReplicaLifecycleSchema} {
		first := export()
		var schema map[string]any
		if err := json.Unmarshal(first, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["$defs"] == nil {
			t.Fatal("missing schema vocabulary or definitions")
		}
		expected := bytes.Clone(first)
		first[0] = '!'
		if !bytes.Equal(expected, export()) {
			t.Fatal("schema export aliases embedded bytes")
		}
	}
	var lifecycle struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(ReplicaLifecycleSchema(), &lifecycle); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"registerRequest", "registerResponse", "renewRequest", "renewResponse", "deregisterRequest", "deregisterResponse"} {
		if len(lifecycle.Defs[name]) == 0 {
			t.Fatalf("missing lifecycle schema definition %s", name)
		}
	}
}

func TestTypedContractValidation(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		contract := newHTTPContract()
		contract.TransportSecurity.ClientCertificateRequired = false
		if contract.validate() == nil {
			t.Fatal("accepted missing mTLS")
		}
		contract = newHTTPContract()
		delete(contract.Plugin.Endpoints, "reload")
		if _, err := contract.Endpoint("reload"); err == nil {
			t.Fatal("resolved missing reload endpoint")
		}
		if _, err := newHTTPContract().StatusForOutcome("invented"); err == nil {
			t.Fatal("accepted unbounded outcome")
		}
	})
	t.Run("replica", func(t *testing.T) {
		contract := newReplicaLifecycleContract()
		contract.Lease.RenewIntervalSeconds = contract.Lease.TTLSeconds
		if contract.Validate() == nil {
			t.Fatal("accepted invalid lease timing")
		}
		contract = newReplicaLifecycleContract()
		contract.ReleaseCohortCompatibility.MissingEvidence = "compatible"
		if contract.Validate() == nil {
			t.Fatal("accepted fail-open release evidence")
		}
	})
	t.Run("poll", func(t *testing.T) {
		contract := newPeerDirectoryPollContract()
		contract.Poll.RequestDeadlineMs = contract.Poll.MaximumWaitMs
		if contract.Validate() == nil {
			t.Fatal("accepted insufficient request deadline")
		}
		contract = newPeerDirectoryPollContract()
		contract.Responses.Directory.CallerMustMatchAuthenticatedIdentity = false
		if contract.Validate() == nil {
			t.Fatal("accepted unauthenticated directory binding")
		}
	})
}
