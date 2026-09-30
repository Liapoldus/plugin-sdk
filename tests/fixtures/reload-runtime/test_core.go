package main

// Fixture-only Core replica.
//
// This file is a test fixture and is never part of a production package. It
// stands in for the Core control plane the SDK talks to: it publishes immutable
// generations for exact-generation pulls and it brokers one-use secret grants.
// Every path, media type, limit, status and code it writes is resolved from the
// loaded contract, so no contract value is spelled here. Its failures are
// injected by the harness through the control surface, which is what makes the
// negative lifecycle outcomes reachable without a second Core implementation.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"liapoldus.local/plugin-sdk/domain/interfaces"
	"liapoldus.local/plugin-sdk/domain/models"
	"liapoldus.local/plugin-sdk/infrastructure"
)

var (
	errTestCore      = errors.New("test core refused the request")
	errTestCoreRoute = errors.New("test core route does not match the contract template")
)

// testCoreFault is one injected failure of the exact-generation pull. Each value
// names a contract outcome of the pull family, so the status and code written
// for it are looked up in the contract rather than chosen here. A value the
// contract does not register would produce a refusal the SDK cannot attribute, so
// the harness vocabulary is deliberately the contract vocabulary.
type testCoreFault string

const (
	testCoreFaultNone        testCoreFault = ""
	testCoreFaultUnknown     testCoreFault = "unknownGeneration"
	testCoreFaultStale       testCoreFault = "staleGeneration"
	testCoreFaultDenied      testCoreFault = "notPermitted"
	testCoreFaultCanceled    testCoreFault = "cancelled"
	testCoreFaultExpired     testCoreFault = "deadlineExceeded"
	testCoreFaultUnavailable testCoreFault = "coreUnavailable"
)

// The two legs of the secret-grant family. These names are the fixture harness's
// own control vocabulary: the contract publishes one family, and the SDK accepts
// different sets of attributable outcomes for an issue and a redemption, so a
// scenario has to name the leg it intends to disturb.
const (
	testSecretLegIssue      = "issue"
	testSecretLegRedemption = "redemption"
)

// testSecretFault is one injected failure of the secret-grant family.
type testSecretFault string

const (
	testSecretFaultNone      testSecretFault = ""
	testSecretFaultDenied    testSecretFault = "grantDenied"
	testSecretFaultUnknown   testSecretFault = "grantUnknown"
	testSecretFaultExpired   testSecretFault = "grantExpired"
	testSecretFaultSpent     testSecretFault = "grantSpent"
	testSecretFaultNotPermit testSecretFault = "notPermitted"
)

// testGeneration is one immutable published generation. The bytes are exactly
// what a pull returns; the announced descriptors are what the response headers
// claim, so a generation can be published whose announced digest or schema
// version deliberately disagrees with its bytes, and malformed marks the one
// deliberate exception to a document having to be a well-formed JSON object.
type testGeneration struct {
	generation             string
	schemaVersion          string
	announcedDigest        string
	announcedSchemaVersion string
	rawJSON                []byte
	state                  interfaces.GenerationState
	malformed              bool
	oversized              bool
}

// testGrant is one issued secret grant held by the fixture Core.
type testGrant struct {
	handle     string
	reference  string
	purpose    string
	generation string
	expiresAt  time.Time
	spent      bool
}

// testCore is the fixture Core replica: an exact-generation document store, a
// secret broker and the two HTTPS routes the SDK dials.
type testCore struct {
	contract infrastructure.HTTPContract
	clock    testClock
	handler  http.Handler

	mutex           sync.Mutex
	generations     map[string]testGeneration
	faults          map[string]testCoreFault
	issueFault      testSecretFault
	redemptionFault testSecretFault
	grants          map[string]testGrant
	handles         int64
}

// newTestCore builds the fixture Core replica and fails closed when the contract
// does not describe the routes or the headers this replica has to serve.
func newTestCore(contract infrastructure.HTTPContract) (*testCore, error) {
	pull := contract.Core.ConfigPull
	grant := contract.Core.SecretGrant
	for _, header := range []string{"generation", "sha256", "schemaVersion", "generationState"} {
		if pull.ResponseHeaders[header] == "" {
			return nil, errTestCoreRoute
		}
	}
	if pull.Method == "" || !strings.HasPrefix(pull.PathTemplate, "/") {
		return nil, errTestCoreRoute
	}
	if grant.Issue.Method == "" || grant.Redemption.Method == "" ||
		!strings.Contains(grant.Redemption.PathTemplate, "{handle}") {
		return nil, errTestCoreRoute
	}
	core := &testCore{
		contract:    contract,
		clock:       testClock{},
		generations: make(map[string]testGeneration),
		faults:      make(map[string]testCoreFault),
		grants:      make(map[string]testGrant),
	}
	mux := http.NewServeMux()
	// Go's pattern syntax resolves the contract's own {generation} and {handle}
	// placeholders, so the fixture serves the contract path rather than a copy of
	// it and reads the opaque identifiers straight out of the request.
	mux.HandleFunc(pull.Method+" "+pull.PathTemplate, core.servePull)
	mux.HandleFunc(grant.Issue.Method+" "+grant.Issue.PathTemplate, core.serveIssue)
	mux.HandleFunc(grant.Redemption.Method+" "+grant.Redemption.PathTemplate, core.serveRedemption)
	core.handler = mux
	return core, nil
}

// servePull answers one exact-generation pull. It never lists generations and
// never substitutes a document for the one requested.
func (core *testCore) servePull(w http.ResponseWriter, r *http.Request) {
	generation := r.PathValue("generation")
	if !models.ValidGeneration(generation) {
		core.refuse(w, models.OutcomeInvalidRequest)
		return
	}
	core.mutex.Lock()
	fault := core.faults[generation]
	stored, published := core.generations[generation]
	core.mutex.Unlock()
	if fault != testCoreFaultNone {
		core.refuse(w, models.Outcome(fault))
		return
	}
	if !published {
		core.refuse(w, models.OutcomeUnknownGeneration)
		return
	}
	pull := core.contract.Core.ConfigPull
	headers := pull.ResponseHeaders
	w.Header().Set(headers["generation"], stored.generation)
	w.Header().Set(headers["sha256"], stored.announcedDigest)
	w.Header().Set(headers["schemaVersion"], stored.announcedSchemaVersion)
	w.Header().Set(headers["generationState"], string(stored.state))
	w.Header().Set("Content-Type", pull.ResponseMediaType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stored.rawJSON)
}

// serveIssue mints one grant bound to the generation the request names.
func (core *testCore) serveIssue(w http.ResponseWriter, r *http.Request) {
	document := core.contract.Core.SecretGrant.Issue
	body, err := readTestDocument(r, document.MaximumRequestBytes)
	if err != nil {
		core.refuse(w, models.OutcomeInvalidRequest)
		return
	}
	var request models.SecretGrantRequest
	if err := strictTestUnmarshal(body, &request); err != nil {
		core.refuse(w, models.OutcomeInvalidRequest)
		return
	}
	core.mutex.Lock()
	fault := core.issueFault
	core.issueFault = testSecretFaultNone
	core.mutex.Unlock()
	if fault != testSecretFaultNone {
		core.refuse(w, models.Outcome(fault))
		return
	}
	if request.Validate() != nil || !models.ValidGeneration(request.Generation) {
		core.refuse(w, models.OutcomeInvalidRequest)
		return
	}
	ttl := time.Duration(core.contract.Deadlines.CoreSecretGrantSeconds) * time.Second
	grant := testGrant{
		handle:     core.nextHandle(),
		reference:  request.Reference,
		purpose:    request.Purpose,
		generation: request.Generation,
		expiresAt:  core.clock.Now().Add(ttl),
	}
	core.mutex.Lock()
	core.grants[grant.handle] = grant
	core.mutex.Unlock()
	core.write(w, http.StatusOK, document.ResponseMediaType, document.MaximumResponseBytes, models.SecretGrant{
		Handle:     grant.handle,
		Reference:  grant.reference,
		Purpose:    grant.purpose,
		Generation: grant.generation,
		ExpiresAt:  grant.expiresAt.UTC(),
	})
}

// serveRedemption exchanges one handle for the value it names, exactly once.
func (core *testCore) serveRedemption(w http.ResponseWriter, r *http.Request) {
	document := core.contract.Core.SecretGrant.Redemption
	body, err := readTestDocument(r, document.MaximumRequestBytes)
	if err != nil {
		core.refuse(w, models.OutcomeInvalidRequest)
		return
	}
	var redemption models.SecretRedemption
	if err := strictTestUnmarshal(body, &redemption); err != nil {
		core.refuse(w, models.OutcomeInvalidRequest)
		return
	}
	core.mutex.Lock()
	defer core.mutex.Unlock()
	if fault := core.redemptionFault; fault != testSecretFaultNone {
		core.redemptionFault = testSecretFaultNone
		core.refuse(w, models.Outcome(fault))
		return
	}
	grant, known := core.grants[redemption.Handle]
	switch {
	case !known:
		core.refuse(w, models.OutcomeGrantUnknown)
		return
	case grant.spent:
		core.refuse(w, models.OutcomeGrantSpent)
		return
	case !grant.expiresAt.After(core.clock.Now()):
		core.refuse(w, models.OutcomeGrantExpired)
		return
	}
	grant.spent = true
	core.grants[grant.handle] = grant
	// The value document is the plugin's own opaque payload. The SDK does not
	// publish a member for it and hands the bytes on, so the fixture answers with
	// a plain document under the contract media type.
	core.write(w, http.StatusOK, document.ResponseMediaType, document.MaximumResponseBytes, map[string]string{
		"reference": grant.reference,
		"value":     testSecretValue(grant.reference),
	})
}

// nextHandle mints an opaque, single-segment handle that carries no product
// meaning and is never logged, acknowledged or echoed to the harness.
func (core *testCore) nextHandle() string {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	core.handles++
	return fmt.Sprintf("test-grant-%d", core.handles)
}

// testSecretValue is the fixture's private payload for one reference. It exists
// only so a redemption has something to return and destroy.
func testSecretValue(reference string) string {
	return "value-for-" + reference
}

// publish stores one immutable generation. An empty announced descriptor means
// the generation is published truthfully.
func (core *testCore) publish(stored testGeneration) error {
	pull := core.contract.Core.ConfigPull
	if !models.ValidGeneration(stored.generation) {
		return fmt.Errorf("%w: generation name", errTestCore)
	}
	// A document that is not a JSON object is a real state of the store, because
	// reaching the malformed-document outcome requires one. A generation therefore
	// has to be marked malformed on purpose by the harness, so a typo in a
	// scenario's document does not silently become a pull refusal to debug later.
	if !stored.malformed && !models.ValidJSONObject(stored.rawJSON) {
		return fmt.Errorf("%w: document is not a JSON object", errTestCore)
	}
	if stored.announcedDigest == "" {
		stored.announcedDigest = testDigest(stored.rawJSON)
	}
	if stored.announcedSchemaVersion == "" {
		stored.announcedSchemaVersion = stored.schemaVersion
	}
	// A document past the contract's own size limit is the same deliberate
	// exception: reaching the oversized-document outcome requires a Core that
	// serves one, and the store is the only place that can hold it. It is opted
	// into by the harness for the same reason as malformed, so a scenario can
	// never mistake an accidental oversized document for a real refusal.
	if !stored.oversized && int64(len(stored.rawJSON)) > pull.MaximumBytes {
		return fmt.Errorf("%w: document past the contract limit", errTestCore)
	}
	known := false
	for _, state := range pull.GenerationStates {
		if state == string(stored.state) {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("%w: generation state", errTestCore)
	}
	core.mutex.Lock()
	defer core.mutex.Unlock()
	core.generations[stored.generation] = stored
	return nil
}

// armFault installs a one-shot or standing pull failure for one generation. An
// empty generation arms nothing; a fault stays armed until it fires.
func (core *testCore) armFault(generation string, fault testCoreFault) {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	if fault == testCoreFaultNone {
		delete(core.faults, generation)
		return
	}
	core.faults[generation] = fault
}

// armSecretFault installs a one-shot failure for the next grant issue.
// armSecretFault injects one failure into a single leg of the secret-grant
// family. The leg is the harness's own control vocabulary rather than a contract
// string, because the two legs are the same contract family but do not accept the
// same set of attributable outcomes: a replay, an expiry and an unknown handle are
// answers to a redemption, while a denial is an answer to an issue. Arming the
// wrong leg would make the SDK refuse to attribute the answer, which is correct
// behaviour and useless as a test.
func (core *testCore) armSecretFault(redemption bool, fault testSecretFault) {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	if fault == testSecretFaultNone {
		core.issueFault = testSecretFaultNone
		core.redemptionFault = testSecretFaultNone
		return
	}
	if redemption {
		core.redemptionFault = fault
		return
	}
	core.issueFault = fault
}

// refuse writes the contract status and code one outcome owns, plus the outcome
// name itself, so the SDK resolver can attribute the answer from either field.
func (core *testCore) refuse(w http.ResponseWriter, outcome models.Outcome) {
	problem, err := core.contract.StatusForOutcome(string(outcome))
	if err != nil {
		// A contract that describes no answer for an outcome is a broken asset.
		// The fixture cannot invent one, so it fails the request closed.
		http.Error(w, "contract has no problem for this outcome", http.StatusInternalServerError)
		return
	}
	core.write(w, problem.Status, core.contract.Plugin.Responses.ContentTypes.JSON, 0, models.Problem{
		Outcome: outcome,
		Code:    problem.Code,
	})
}

// write emits one JSON document under the contract media type at one status.
func (core *testCore) write(w http.ResponseWriter, status int, mediaType string, maximum int64, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		http.Error(w, "document could not be encoded", http.StatusInternalServerError)
		return
	}
	if maximum > 0 && int64(len(body)) > maximum {
		http.Error(w, "document past the contract limit", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
