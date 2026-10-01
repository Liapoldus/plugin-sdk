package main

// Fixture-only harness control surface.
//
// This file is a test fixture and is never part of a production package. It is
// the only plaintext listener in the fixture: the two production surfaces, the
// plugin REST surface and the Core control API, are both mutual TLS only, and the
// fixture does not weaken either of them. This listener exists so a test harness
// can drive scenarios the SDK itself never initiates, such as injecting a
// Core-side pull failure, redeeming a grant twice, or asking what the plugin is
// currently serving. It runs on the loopback interface, it serves no contract
// route, and it never writes a handle, a value or a document into its answers.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

var errTestControl = errors.New("test control operation refused")

// testControlMaximumRequestBytes bounds a control request body. It is deliberately
// larger than the contract's own document limit: the control surface is how a
// harness hands the Core stand-in a configuration document, so a control limit
// below the contract limit would silently make the contract's own
// documentOversized boundary unreachable, and a test could then never prove that
// the SDK refuses an oversized document at the size the contract actually names.
const testControlMaximumRequestBytes = 1 << 20

// testControl is the harness control surface. It holds the pieces a scenario
// needs: the fixture Core replica, the Core-side plugin client, the plugin's
// secret use case and the plugin's own applier state.
type testControl struct {
	core      *testCore
	contract  infrastructure.HTTPContract
	client    *infrastructure.PluginClient
	secrets   *testSecrets
	applier   *testApplier
	scenarios *testScenarios
	handler   http.Handler
}

// newTestControl builds the control mux.
func newTestControl(core *testCore, contract infrastructure.HTTPContract,
	client *infrastructure.PluginClient, secrets *testSecrets,
	applier *testApplier, scenarios *testScenarios) *testControl {
	control := &testControl{core: core, contract: contract, client: client,
		secrets: secrets, applier: applier, scenarios: scenarios}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+testRouteControlReload, control.handleReload)
	mux.HandleFunc("POST "+testRouteControlPublish, control.handlePublish)
	mux.HandleFunc("POST "+testRouteControlFault, control.handleFault)
	mux.HandleFunc("POST "+testRouteControlSecretFault, control.handleSecretFault)
	mux.HandleFunc("POST "+testRouteControlSecret, control.handleSecret)
	mux.HandleFunc("POST "+testRouteControlState, control.handleState)
	mux.HandleFunc("POST "+testRouteControlDocument, control.handleDocument)
	mux.HandleFunc("POST "+testRouteControlDrops, control.handleDrops)
	mux.HandleFunc("POST "+testRouteControlConnections, control.handleConnections)
	mux.HandleFunc("POST "+testRouteControlRotation, control.handleRotation)
	mux.HandleFunc("POST "+testRouteControlLoad, control.handleLoad)
	mux.HandleFunc("POST "+testRouteControlReconnect, control.handleReconnect)
	control.handler = mux
	return control
}

// handleReload drives one Core-issued reload through the production plugin
// client, so the answer is the acknowledgement the contract publishes and the
// status a Core would observe.
func (control *testControl) handleReload(w http.ResponseWriter, r *http.Request) {
	var request models.Reload
	if !control.decode(w, r, &request) {
		return
	}
	acknowledgement, err := control.client.Reload(r.Context(), request)
	outcome := acknowledgement.Outcome
	status := http.StatusOK
	if err != nil {
		// The adapter reports only what the call actually saw. An empty
		// classification means the failure was not attributable to a problem the
		// peer published, so the harness reports no outcome rather than inventing
		// one.
		classified := control.client.ClassifyOutcome(err)
		outcome = classified
		status = control.statusFor(classified)
	}
	control.write(w, status, testReloadResult{
		Generation:    acknowledgement.Generation,
		SHA256:        acknowledgement.SHA256,
		SchemaVersion: acknowledgement.SchemaVersion,
		Applied:       acknowledgement.Applied,
		Outcome:       string(outcome),
	})
}

// handlePublish stores one immutable generation in the fixture Core. The
// announced descriptors may be left out, in which case the generation is
// published truthfully, or set to something else, which is how a scenario reaches
// a digest or schema-version mismatch without corrupting any stored document.
func (control *testControl) handlePublish(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Generation             string `json:"generation"`
		SchemaVersion          string `json:"schemaVersion"`
		RawJSON                string `json:"rawJSON"`
		State                  string `json:"state"`
		AnnouncedDigest        string `json:"announcedDigest"`
		AnnouncedSchemaVersion string `json:"announcedSchemaVersion"`
		Malformed              bool   `json:"malformed"`
		Oversized              bool   `json:"oversized"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	if request.State == "" {
		request.State = string(interfaces.GenerationStateActive)
	}
	if request.SchemaVersion == "" {
		request.SchemaVersion = testSchemaVersion
	}
	state, ok := testGenerationState(request.State)
	if !ok {
		control.fail(w, fmt.Errorf("%w: unknown generation state %q", errTestControl, request.State))
		return
	}
	err := control.core.publish(testGeneration{
		generation:             request.Generation,
		schemaVersion:          request.SchemaVersion,
		announcedDigest:        request.AnnouncedDigest,
		announcedSchemaVersion: request.AnnouncedSchemaVersion,
		rawJSON:                []byte(request.RawJSON),
		state:                  state,
		malformed:              request.Malformed,
		oversized:              request.Oversized,
	})
	if err != nil {
		control.fail(w, err)
		return
	}
	control.write(w, http.StatusOK, map[string]any{
		"operation":  "publish",
		"generation": request.Generation,
		"state":      request.State,
		"digest":     testDigest([]byte(request.RawJSON)),
	})
}

// handleFault arms one pull failure for one generation.
func (control *testControl) handleFault(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Generation string `json:"generation"`
		Mode       string `json:"mode"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	fault := testCoreFault(request.Mode)
	switch fault {
	case testCoreFaultNone, testCoreFaultUnknown, testCoreFaultStale, testCoreFaultDenied,
		testCoreFaultCanceled, testCoreFaultExpired, testCoreFaultUnavailable:
	default:
		control.fail(w, fmt.Errorf("%w: unknown fault %q", errTestControl, request.Mode))
		return
	}
	control.core.armFault(request.Generation, fault)
	control.write(w, http.StatusOK, map[string]any{
		"operation":  "fault",
		"generation": request.Generation,
		"mode":       request.Mode,
	})
}

// handleSecretFault arms a one-shot failure for one leg of the secret-grant
// family. The leg is the harness's own control vocabulary, not a contract string:
// the two legs belong to the same contract family but do not accept the same set
// of attributable outcomes, so an answer injected into the wrong leg would be
// refused as unattributable and would prove nothing.
func (control *testControl) handleSecretFault(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Leg  string `json:"leg"`
		Mode string `json:"mode"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	redemption, err := secretFaultLeg(request.Leg)
	if err != nil {
		control.fail(w, err)
		return
	}
	fault := testSecretFault(request.Mode)
	switch fault {
	case testSecretFaultNone, testSecretFaultDenied, testSecretFaultUnknown,
		testSecretFaultExpired, testSecretFaultSpent, testSecretFaultNotPermit:
	default:
		control.fail(w, fmt.Errorf("%w: unknown secret fault %q", errTestControl, request.Mode))
		return
	}
	control.core.armSecretFault(redemption, fault)
	control.write(w, http.StatusOK, map[string]any{
		"operation": "secretFault",
		"leg":       request.Leg,
		"mode":      request.Mode,
	})
}

// secretFaultLeg maps the harness's leg name onto the leg it arms. An empty mode
// clears both legs, so a scenario can disarm the fixture without a second request.
func secretFaultLeg(leg string) (bool, error) {
	switch leg {
	case "":
		return false, nil
	case testSecretLegIssue:
		return false, nil
	case testSecretLegRedemption:
		return true, nil
	default:
		return false, fmt.Errorf("%w: unknown secret fault leg %q", errTestControl, leg)
	}
}

// handleSecret drives the secret scenarios a harness can observe without ever
// seeing a handle or a value.
func (control *testControl) handleSecret(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Reference string `json:"reference"`
		Purpose   string `json:"purpose"`
		Operation string `json:"operation"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	if request.Reference == "" || request.Purpose == "" {
		control.fail(w, fmt.Errorf("%w: a secret operation needs a reference and a purpose", errTestControl))
		return
	}
	switch request.Operation {
	case "issue":
		outcome, err := control.secrets.issue(r.Context(), request.Reference, request.Purpose)
		control.write(w, http.StatusOK, map[string]any{
			"operation": "issue", "outcome": outcome, "failure": testErrorName(err),
		})
	case "provide":
		length, digest, err := control.secrets.provide(r.Context(), request.Reference, request.Purpose)
		control.write(w, http.StatusOK, map[string]any{
			"operation": "provide", "bytes": length, "valueSHA256": digest,
			"failure": testErrorName(err),
		})
	case "redeemTwice":
		first, second, err := control.secrets.redeemTwice(r.Context(), request.Reference, request.Purpose)
		control.write(w, http.StatusOK, map[string]any{
			"operation": "redeemTwice", "first": first, "second": second,
			"failure": testErrorName(err),
		})
	default:
		control.fail(w, fmt.Errorf("%w: unknown secret operation %q", errTestControl, request.Operation))
	}
}

// handleState reports what the plugin is actually serving, which is how a
// scenario proves that a refused apply left the previous generation active.
func (control *testControl) handleState(w http.ResponseWriter, r *http.Request) {
	readiness, readinessErr := control.client.Readiness(r.Context())
	registration, identityErr := control.client.Identity(r.Context())
	active, hasActive := control.applier.activeConfiguration()
	applied, refused := control.applier.counters()
	document := map[string]any{
		"operation":                 "state",
		"readiness":                 readiness,
		"readinessFailure":          testErrorName(readinessErr),
		"identity":                  registration,
		"identityFailure":           testErrorName(identityErr),
		"applyApplied":              applied,
		"applyRefused":              refused,
		"candidateSecretGeneration": control.applier.lastCandidateSecretGeneration(),
		"activeGeneration":          "",
		"activeDigest":              "",
	}
	if hasActive {
		document["activeGeneration"] = active.Generation
		document["activeDigest"] = active.SHA256
	}
	control.write(w, http.StatusOK, document)
}

// handleDocument returns one plugin-owned document, so a scenario can check that
// the manifest and the configuration schema are served verbatim.
func (control *testControl) handleDocument(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	switch request.Name {
	case "manifest":
		document, err := control.client.Manifest(r.Context())
		control.write(w, http.StatusOK, map[string]any{
			"operation": "document", "name": request.Name,
			"document": json.RawMessage(document), "failure": testErrorName(err),
		})
	case "configurationSchema":
		document, err := control.client.ConfigurationSchema(r.Context())
		control.write(w, http.StatusOK, map[string]any{
			"operation": "document", "name": request.Name,
			"document": json.RawMessage(document), "failure": testErrorName(err),
		})
	case "metrics":
		text, err := control.client.Metrics(r.Context())
		control.write(w, http.StatusOK, map[string]any{
			"operation": "document", "name": request.Name,
			"text": text, "failure": testErrorName(err),
		})
	default:
		control.fail(w, fmt.Errorf("%w: unknown document %q", errTestControl, request.Name))
	}
}

// statusFor resolves the status Core would see for one outcome, from the
// contract, so the harness never states a status of its own.
func (control *testControl) statusFor(outcome models.Outcome) int {
	if !outcome.Valid() {
		return http.StatusInternalServerError
	}
	problem, err := control.contract.StatusForOutcome(string(outcome))
	if err != nil {
		return http.StatusInternalServerError
	}
	return problem.Status
}

// decode reads one bounded JSON control request.
func (control *testControl) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := readTestDocument(r, testControlMaximumRequestBytes)
	if err != nil {
		control.fail(w, fmt.Errorf("%w: %v", errTestControl, err))
		return false
	}
	if err := strictTestUnmarshal(body, target); err != nil {
		control.fail(w, fmt.Errorf("%w: %v", errTestControl, err))
		return false
	}
	return true
}

// write emits one control answer.
func (control *testControl) write(w http.ResponseWriter, status int, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		http.Error(w, "control document could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", testControlMediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// fail reports a harness mistake as a fixture error, never as a contract answer.
func (control *testControl) fail(w http.ResponseWriter, err error) {
	control.write(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

// testReloadResult is the control answer for one reload: the acknowledgement the
// contract publishes, plus the status a Core would observe.
type testReloadResult struct {
	Generation    string `json:"generation"`
	SHA256        string `json:"sha256"`
	SchemaVersion string `json:"schemaVersion"`
	Applied       bool   `json:"applied"`
	Outcome       string `json:"outcome"`
}

// readTestDocument reads at most maximum bytes of request body, and reports a
// body past the limit rather than truncating it.
func readTestDocument(r *http.Request, maximum int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("request has no body")
	}
	if maximum <= 0 {
		return nil, errors.New("no limit configured")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum {
		return nil, errors.New("request body past the limit")
	}
	return body, nil
}

// strictTestUnmarshal decodes one JSON object with no unknown field and no
// duplicate key, so a control request cannot smuggle in a field the fixture
// ignores.
//
// The duplicate-key and single-object rules come from the shipped validator, which
// walks the document's tokens rather than unmarshalling it. The unknown-field rule
// has no shipped equivalent, because the control surface is the fixture's own and
// not a published contract, so it is enforced here with a strict decoder.
func strictTestUnmarshal(body []byte, target any) error {
	if !models.ValidJSONObject(body) {
		return errors.New("request body is not a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body is not one JSON object")
	}
	return nil
}

// testDigest is the SHA-256 of a document in the form the contract names.
func testDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

// testErrorName reports a failure by identity only, so no control answer can
// carry internal detail, an address, a path or a document.
func testErrorName(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, models.ErrNotReady):
		return "notReady"
	case errors.Is(err, models.ErrInvalidSecretGrant):
		return "invalidSecretGrant"
	case errors.Is(err, models.ErrInvalidConfiguration),
		errors.Is(err, models.ErrInvalidDocument):
		return "invalidConfiguration"
	case errors.Is(err, models.ErrInvalidReload):
		return "invalidReload"
	default:
		return "failed"
	}
}

// testGenerationState converts the harness spelling of a durable slot into the
// domain type, refusing anything the domain does not register.
func testGenerationState(value string) (interfaces.GenerationState, bool) {
	switch interfaces.GenerationState(value) {
	case interfaces.GenerationStateActive:
		return interfaces.GenerationStateActive, true
	case interfaces.GenerationStatePrevious:
		return interfaces.GenerationStatePrevious, true
	default:
		return "", false
	}
}

// handleDrops arms or disarms the connection-drop switch. Arming a count of zero
// disarms it, and either way the answer reports what actually landed, so a harness
// can tell an injection that happened from one it merely asked for.
func (control *testControl) handleDrops(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Count int `json:"count"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	if control.scenarios == nil {
		control.fail(w, fmt.Errorf("%w: no transport faults are wired", errTestControl))
		return
	}
	armed, dropped := control.scenarios.inputs.drops.arm(request.Count)
	control.write(w, http.StatusOK, map[string]any{
		"operation": "drops",
		"armed":     armed,
		"remaining": control.scenarios.inputs.drops.remainingDrops(),
		"dropped":   dropped,
	})
}

// handleConnections reports the plugin listener's own connection bookkeeping. The
// read settles first, so a count is only published once it has stopped moving.
func (control *testControl) handleConnections(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if !control.decode(w, r, &request) {
		return
	}
	if control.scenarios == nil {
		control.fail(w, fmt.Errorf("%w: no transport faults are wired", errTestControl))
		return
	}
	// A successful recovery call may legitimately leave an HTTP keep-alive
	// connection idle. Close that reusable connection before checking whether
	// the deliberately dropped connections themselves were released.
	control.client.CloseIdleConnections()
	connections := control.scenarios.inputs.connections
	settled := connections.settle(
		testSeconds(control.contract.Deadlines.PluginShutdownGraceSeconds) / testLoadDrainGraceDivisor)
	control.write(w, http.StatusOK, map[string]any{
		"operation": "connections",
		"opened":    settled.opened,
		"open":      settled.open,
		"hijacked":  settled.hijacked,
	})
}

// handleRotation drives the rotating surface. Starting, rotating and stopping are
// three operations on one listener rather than three surfaces, so a harness that
// starts one and stops it is describing a rotation of a surface that really served.
func (control *testControl) handleRotation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Operation string `json:"operation"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	if control.scenarios == nil {
		control.fail(w, fmt.Errorf("%w: no scenarios are wired", errTestControl))
		return
	}
	surface, err := control.scenarios.rotationSurface()
	if err != nil {
		control.fail(w, fmt.Errorf("%w: %v", errTestControl, err))
		return
	}
	switch request.Operation {
	case "start":
		address, startErr := surface.start()
		if startErr != nil {
			control.fail(w, startErr)
			return
		}
		control.write(w, http.StatusOK, map[string]any{
			"operation": request.Operation,
			"url":       address,
		})
	case "rotate":
		providerBefore, providerAfter, servedBefore, servedAfter, rotateErr := surface.rotate()
		if rotateErr != nil {
			control.fail(w, rotateErr)
			return
		}
		control.write(w, http.StatusOK, map[string]any{
			"operation":            request.Operation,
			"providerSerialBefore": providerBefore,
			"providerSerialAfter":  providerAfter,
			"servedSerialBefore":   servedBefore,
			"servedSerialAfter":    servedAfter,
		})
	case "stop":
		closed, removed, stopErr := surface.stop()
		answer := map[string]any{
			"operation": request.Operation,
			"closed":    closed,
			"removed":   removed,
		}
		if stopErr != nil {
			answer["failure"] = stopErr.Error()
		}
		control.write(w, http.StatusOK, answer)
	default:
		control.fail(w, fmt.Errorf("%w: unknown rotation operation", errTestControl))
	}
}

// handleLoad starts the loaded surface or drains it. The drain answer reports what
// the production client actually received rather than what the fixture believes it
// served, so a truncated or interleaved response is visible.
func (control *testControl) handleLoad(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Operation string `json:"operation"`
	}
	if !control.decode(w, r, &request) {
		return
	}
	if control.scenarios == nil {
		control.fail(w, fmt.Errorf("%w: no scenarios are wired", errTestControl))
		return
	}
	surface, err := control.scenarios.loadSurface()
	if err != nil {
		control.fail(w, fmt.Errorf("%w: %v", errTestControl, err))
		return
	}
	switch request.Operation {
	case "start":
		address, startErr := surface.start()
		if startErr != nil {
			control.fail(w, startErr)
			return
		}
		control.write(w, http.StatusOK, map[string]any{"operation": request.Operation, "url": address})
	case "shutdown":
		report := surface.shutdown()
		answer := map[string]any{
			"operation":           request.Operation,
			"inFlight":            report.inFlight,
			"served":              report.served,
			"elapsedMilliseconds": report.elapsedMilliseconds,
			"afterShutdown":       report.afterShutdown,
			"bodyBytes":           report.bodyBytes,
			"bodySHA256":          report.bodySHA256,
			"status":              report.status,
		}
		if report.failure != "" {
			answer["failure"] = report.failure
		}
		control.write(w, http.StatusOK, answer)
	default:
		control.fail(w, fmt.Errorf("%w: unknown load operation", errTestControl))
	}
}

// handleReconnect measures one close race against the running plugin listener. It
// takes no count: the number of connections is the scenario's own, and a harness
// that could choose it could choose a number too small to race anything.
func (control *testControl) handleReconnect(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if !control.decode(w, r, &request) {
		return
	}
	if control.scenarios == nil {
		control.fail(w, fmt.Errorf("%w: no scenarios are wired", errTestControl))
		return
	}
	churn, err := control.scenarios.churnScenario()
	if err != nil {
		control.fail(w, fmt.Errorf("%w: %v", errTestControl, err))
		return
	}
	report := churn.run()
	answer := map[string]any{
		"operation":        "churn",
		"requested":        report.requested,
		"handshakes":       report.handshakes,
		"refused":          report.refused,
		"statusBefore":     report.statusBefore,
		"bodyBytesBefore":  report.bytesBefore,
		"bodySHA256Before": report.sha256Before,
		"statusAfter":      report.statusAfter,
		"bodyBytesAfter":   report.bytesAfter,
		"bodySHA256After":  report.sha256After,
		"openedBefore":     report.openedBefore,
		"openedAfter":      report.openedAfter,
		"open":             report.open,
		"hijackedAfter":    report.hijackedAfter,
	}
	if report.failure != "" {
		answer["failure"] = report.failure
	}
	control.write(w, http.StatusOK, answer)
}
