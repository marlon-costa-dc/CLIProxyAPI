package test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelrouting"
)

const (
	modelPipelineFixtureSHA = "c37875df4ea6d89a49c39c862febfcc5656eaf51ec7966b76560b0c9b3f541ee"
	modelPipelineSnapshot   = "sha256:15303dbab83d64d09f79f1f3a22bc09fb3ad5916f2624283f2c6a0ecbe969801"
	modelPipelineProjection = "sha256:a2d543504bba7caa9a5c925bb1018e484a0331fb0479dbc87e828db51bc275a5"
)

type modelPipelineEnvelopeContract struct {
	ModelPipeline struct {
		SchemaVersion int             `json:"schema_version"`
		Snapshot      json.RawMessage `json:"snapshot"`
	} `json:"model_pipeline"`
}

type modelPipelineSnapshotContract struct {
	SchemaVersion    int                         `json:"schema_version"`
	Generation       uint64                      `json:"generation"`
	GeneratedAt      string                      `json:"generated_at"`
	SourceDigests    json.RawMessage             `json:"source_digests"`
	Inventory        json.RawMessage             `json:"inventory"`
	Catalog          json.RawMessage             `json:"catalog"`
	Observations     json.RawMessage             `json:"observations"`
	Evaluations      json.RawMessage             `json:"evaluations"`
	Rejections       json.RawMessage             `json:"rejections"`
	Assignments      []modelPipelineAssignment   `json:"assignments"`
	AgentBindings    []modelPipelineAgentBinding `json:"agent_bindings"`
	FailurePolicy    modelPipelineFailurePolicy  `json:"failure_policy"`
	Publication      modelPipelinePublication    `json:"publication"`
	SnapshotDigest   string                      `json:"snapshot_digest"`
	ProjectionDigest string                      `json:"projection_digest"`
}

type modelPipelineAssignment struct {
	TierID     string          `json:"tier_id"`
	Alias      string          `json:"alias"`
	Selectable bool            `json:"selectable"`
	Reason     string          `json:"reason"`
	Candidates json.RawMessage `json:"candidates"`
}

type modelPipelineAgentBinding struct {
	Agent  string `json:"agent"`
	TierID string `json:"tier_id"`
	Alias  string `json:"alias"`
}

type modelPipelineFailurePolicy struct {
	Mode                                string                      `json:"mode"`
	CredentialAcquisitionTimeoutSeconds int                         `json:"credential_acquisition_timeout_seconds"`
	AutomaticRetry                      bool                        `json:"automatic_retry"`
	AutomaticFailover                   bool                        `json:"automatic_failover"`
	MaxCandidateAttempts                int                         `json:"max_candidate_attempts"`
	FailoverRules                       []modelPipelineFailoverRule `json:"failover_rules"`
	ServeStaleOnError                   bool                        `json:"serve_stale_on_error"`
	PreserveFirstError                  bool                        `json:"preserve_first_error"`
	TerminateOwnedRequestOnCancel       bool                        `json:"terminate_owned_request_on_cancel"`
}

type modelPipelineFailoverRule struct {
	RuleID       string   `json:"rule_id"`
	HTTPStatuses []int    `json:"http_statuses"`
	ErrorCodes   []string `json:"error_codes"`
	FailureKinds []string `json:"failure_kinds"`
}

type modelPipelinePublication struct {
	Mode                  string                           `json:"mode"`
	Targets               []modelPipelinePublicationTarget `json:"targets"`
	RequestTimeoutSeconds int                              `json:"request_timeout_seconds"`
	RetainedSnapshots     int                              `json:"retained_snapshots"`
}

type modelPipelinePublicationTarget struct {
	TargetID string `json:"target_id"`
	Format   string `json:"format"`
	Location string `json:"location"`
	Required bool   `json:"required"`
}

func TestModelPipelineSnapshotV1FixtureContract(t *testing.T) {
	fixture, errRead := os.ReadFile("fixtures/model-pipeline-snapshot-v1.json")
	if errRead != nil {
		t.Fatalf("read model pipeline fixture: %v", errRead)
	}
	digest := sha256.Sum256(fixture)
	if got := hex.EncodeToString(digest[:]); got != modelPipelineFixtureSHA {
		t.Fatalf("fixture SHA-256 = %s, want %s", got, modelPipelineFixtureSHA)
	}

	var envelope modelPipelineEnvelopeContract
	decodeJSONStrict(t, fixture, &envelope)
	if envelope.ModelPipeline.SchemaVersion != 1 {
		t.Fatalf("model_pipeline.schema_version = %d, want 1", envelope.ModelPipeline.SchemaVersion)
	}

	assertExactJSONKeys(t, envelope.ModelPipeline.Snapshot, []string{
		"agent_bindings", "assignments", "catalog", "evaluations", "failure_policy", "generated_at", "generation",
		"inventory", "observations", "projection_digest", "publication", "rejections", "schema_version", "snapshot_digest", "source_digests",
	})
	var snapshot modelPipelineSnapshotContract
	decodeJSONStrict(t, envelope.ModelPipeline.Snapshot, &snapshot)
	if snapshot.SchemaVersion != 1 || snapshot.Generation == 0 {
		t.Fatalf("snapshot version/generation = %d/%d", snapshot.SchemaVersion, snapshot.Generation)
	}
	if snapshot.SnapshotDigest != modelPipelineSnapshot || snapshot.ProjectionDigest != modelPipelineProjection {
		t.Fatalf("snapshot digests = %s/%s", snapshot.SnapshotDigest, snapshot.ProjectionDigest)
	}

	assertFailurePolicyContract(t, snapshot.FailurePolicy)
	assertAgentBindingContract(t, snapshot.Assignments, snapshot.AgentBindings)
	assertPublicationContract(t, snapshot.Publication)

	roundTrip, errMarshal := json.Marshal(snapshot)
	if errMarshal != nil {
		t.Fatalf("marshal snapshot contract: %v", errMarshal)
	}
	assertExactJSONKeys(t, roundTrip, []string{
		"agent_bindings", "assignments", "catalog", "evaluations", "failure_policy", "generated_at", "generation",
		"inventory", "observations", "projection_digest", "publication", "rejections", "schema_version", "snapshot_digest", "source_digests",
	})
	var roundTripObject map[string]json.RawMessage
	decodeJSONStrict(t, roundTrip, &roundTripObject)
	assertExactJSONKeys(t, roundTripObject["publication"], []string{"mode", "request_timeout_seconds", "retained_snapshots", "targets"})
}

func assertFailurePolicyContract(t *testing.T, policy modelPipelineFailurePolicy) {
	t.Helper()
	rules := make([]modelrouting.FailoverRule, len(policy.FailoverRules))
	for index, rule := range policy.FailoverRules {
		kinds := make([]modelrouting.FailureKind, len(rule.FailureKinds))
		for kindIndex, kind := range rule.FailureKinds {
			kinds[kindIndex] = modelrouting.FailureKind(kind)
		}
		rules[index] = modelrouting.FailoverRule{
			RuleID: rule.RuleID, HTTPStatuses: rule.HTTPStatuses, ErrorCodes: rule.ErrorCodes, FailureKinds: kinds,
		}
	}
	runtimePolicy := modelrouting.FailurePolicy{
		Mode:                                policy.Mode,
		CredentialAcquisitionTimeoutSeconds: policy.CredentialAcquisitionTimeoutSeconds,
		AutomaticRetry:                      policy.AutomaticRetry,
		AutomaticFailover:                   policy.AutomaticFailover,
		MaxCandidateAttempts:                policy.MaxCandidateAttempts,
		FailoverRules:                       rules,
		ServeStaleOnError:                   policy.ServeStaleOnError,
		PreserveFirstError:                  policy.PreserveFirstError,
		TerminateOwnedRequestOnCancel:       policy.TerminateOwnedRequestOnCancel,
	}
	if runtimePolicy.Mode != "classified_candidate_failover" || runtimePolicy.CredentialAcquisitionTimeoutSeconds != 120 ||
		runtimePolicy.AutomaticRetry || !runtimePolicy.AutomaticFailover || runtimePolicy.MaxCandidateAttempts != 3 ||
		len(runtimePolicy.FailoverRules) != 2 || runtimePolicy.ServeStaleOnError ||
		!runtimePolicy.PreserveFirstError || !runtimePolicy.TerminateOwnedRequestOnCancel {
		t.Fatalf("failure policy does not match the CLIProxy executor contract: %+v", runtimePolicy)
	}
}

func assertAgentBindingContract(t *testing.T, assignments []modelPipelineAssignment, bindings []modelPipelineAgentBinding) {
	t.Helper()
	if len(bindings) == 0 {
		t.Fatal("agent_bindings must not be empty")
	}
	assigned := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		assigned[assignment.TierID] = assignment.Alias
	}
	for index, binding := range bindings {
		if binding.Agent == "" || binding.TierID == "" || binding.Alias == "" {
			t.Fatalf("agent_bindings[%d] has an empty identity: %+v", index, binding)
		}
		if alias, exists := assigned[binding.TierID]; !exists || alias != binding.Alias {
			t.Fatalf("agent_bindings[%d] does not reference its assignment: %+v", index, binding)
		}
	}
}

func assertPublicationContract(t *testing.T, publication modelPipelinePublication) {
	t.Helper()
	if publication.Mode != "atomic_replace" || publication.RequestTimeoutSeconds != 120 || publication.RetainedSnapshots != 2 {
		t.Fatalf("publication contract = %+v", publication)
	}
	if len(publication.Targets) == 0 {
		t.Fatal("publication.targets must not be empty")
	}
	for index, target := range publication.Targets {
		if target.TargetID == "" || target.Format == "" || target.Location == "" {
			t.Fatalf("publication.targets[%d] has an empty identity: %+v", index, target)
		}
	}
}

func assertExactJSONKeys(t *testing.T, raw []byte, want []string) {
	t.Helper()
	var object map[string]json.RawMessage
	decodeJSONStrict(t, raw, &object)
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %v, want %v", got, want)
	}
}

func decodeJSONStrict(t *testing.T, raw []byte, destination any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(destination); errDecode != nil {
		t.Fatalf("strict JSON decode into %T: %v", destination, errDecode)
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); errTrailing != io.EOF {
		t.Fatalf("strict JSON decode into %T has trailing data: %v", destination, fmt.Sprint(errTrailing))
	}
}
