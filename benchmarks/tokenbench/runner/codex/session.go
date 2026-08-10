package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
	genericrunner "github.com/dkropachev/repo-view/benchmarks/tokenbench/runner"
)

// Finish implements runner.ArmSession. It stops admission, waits for in-flight
// proxy requests, emits two sanitized artifacts after a clean process exit (or
// one distinct partial provider trace for ordinary terminal process states),
// compares the pair after the second clean arm, and resets all private runtime
// directories.
func (session *ArmSession) Finish(
	ctx context.Context,
	request genericrunner.ExecutionRequest,
	raw harness.RawExecution,
) ([]harness.Artifact, error) {
	request = cloneExecutionRequest(request)
	digest, err := digestRequest(request)
	if err != nil {
		return nil, err
	}
	if digest != session.requestSHA256 || !reflect.DeepEqual(request, session.request) {
		return nil, errors.New("Codex arm request changed between BeginArm and Finish")
	}
	if err := session.startFinalization(sessionFinishing); err != nil {
		return nil, err
	}
	defer session.completeFinalization(sessionFinished)
	if err := session.deactivate(); err != nil {
		return nil, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}
	if err := session.waitInflight(ctx); err != nil {
		return nil, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}
	if ordinaryTerminalExecution(raw) {
		partial, partialErr := session.partialTrace()
		var traceRaw []byte
		if partialErr == nil {
			traceRaw, partialErr = json.Marshal(partial)
		}
		if partialErr == nil && (len(traceRaw) == 0 ||
			len(traceRaw) > harnesscodex.MaxResponsesTraceBytes) {
			partialErr = errors.New("sanitized partial Codex Responses trace exceeds its artifact limit")
		}
		if partialErr == nil {
			_, partialErr = harnesscodex.ParsePartialResponsesTrace(traceRaw)
		}
		resetErr := session.lifecycle.resetLayout(ctx)
		if partialErr != nil || resetErr != nil {
			return nil, errors.Join(partialErr, resetErr)
		}
		return []harness.Artifact{{
			Name:      harnesscodex.PartialResponsesTraceArtifactName,
			MediaType: harnesscodex.PartialResponsesTraceMediaType,
			Data:      append([]byte(nil), traceRaw...),
		}}, nil
	}

	trace, err := session.finalTrace()
	if err != nil {
		return nil, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}
	traceRaw, err := json.Marshal(trace)
	if err != nil {
		return nil, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}
	if len(traceRaw) == 0 || len(traceRaw) > harnesscodex.MaxResponsesTraceBytes {
		return nil, errors.Join(
			errors.New("sanitized Codex Responses trace exceeds its artifact limit"),
			session.lifecycle.resetLayout(ctx),
		)
	}
	parsedTrace, err := harnesscodex.ParseResponsesTrace(traceRaw)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("validate sanitized Codex Responses trace: %w", err),
			session.lifecycle.resetLayout(ctx),
		)
	}
	configRaw, err := session.lifecycle.readEffectiveConfig(ctx)
	if err != nil {
		return nil, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}
	var registration *harness.MCPServer
	if session.request.Arm == genericrunner.CandidateArm {
		candidate := session.request.Invocation.MCPServers[0]
		registration = &candidate
	}
	configCommon, configDelta, err := normalizeEffectiveConfig(
		configRaw,
		session.request.Arm,
		registration,
	)
	if err != nil {
		return nil, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}

	snapshot := armSnapshot{
		trace:        parsedTrace,
		config:       append([]byte(nil), configRaw...),
		configCommon: append([]byte(nil), configCommon...),
		configDelta:  append([]byte(nil), configDelta...),
	}
	if err := session.lifecycle.storeAndCompare(session.request, snapshot); err != nil {
		return nil, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}
	if err := session.lifecycle.resetLayout(ctx); err != nil {
		return nil, err
	}
	return []harness.Artifact{
		{
			Name:      harnesscodex.ResponsesTraceArtifactName,
			MediaType: harnesscodex.ResponsesTraceMediaType,
			Data:      append([]byte(nil), traceRaw...),
		},
		{
			Name:      harnesscodex.EffectiveConfigArtifactName,
			MediaType: harnesscodex.EffectiveConfigMediaType,
			Data:      append([]byte(nil), configRaw...),
		},
	}, nil
}

func ordinaryTerminalExecution(raw harness.RawExecution) bool {
	return raw.LaunchFailed || raw.TimedOut || raw.Cancelled ||
		raw.StdoutTruncated || raw.StderrTruncated || raw.ExitCode != 0
}

// Abort implements runner.ArmSession. It is idempotent, closes admission,
// waits using the caller's independent cleanup context, and resets only the
// lifecycle's claimed state tree.
func (session *ArmSession) Abort(ctx context.Context) error {
	session.mu.Lock()
	switch session.state {
	case sessionFinished, sessionAborted:
		session.mu.Unlock()
		return nil
	case sessionFinishing:
		session.mu.Unlock()
		return errors.New("Codex arm is currently finishing")
	case sessionActive:
		session.state = sessionAborted
	}
	session.mu.Unlock()
	deactivateErr := session.deactivate()
	waitErr := session.waitInflight(ctx)
	resetErr := session.lifecycle.resetLayout(ctx)
	return errors.Join(deactivateErr, waitErr, resetErr)
}

func (session *ArmSession) startFinalization(next sessionState) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != sessionActive {
		return errors.New("Codex arm session is already consumed")
	}
	session.state = next
	return nil
}

func (session *ArmSession) completeFinalization(next sessionState) {
	session.mu.Lock()
	session.state = next
	session.mu.Unlock()
}

func (session *ArmSession) deactivate() error {
	lifecycle := session.lifecycle
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.active == nil {
		return nil
	}
	if lifecycle.active != session {
		return errors.New("another Codex arm replaced the active session")
	}
	lifecycle.active = nil
	return nil
}

func (session *ArmSession) waitInflight(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		session.inflight.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (session *ArmSession) finalTrace() (harnesscodex.ResponsesTrace, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.fatal != nil {
		return harnesscodex.ResponsesTrace{}, session.fatal
	}
	if session.trace.FirstRequest == nil || session.requestCount == 0 {
		return harnesscodex.ResponsesTrace{}, errors.New("Codex arm made no completed Responses request")
	}
	if len(session.responses) != session.requestCount {
		return harnesscodex.ResponsesTrace{}, fmt.Errorf(
			"Codex arm completed %d of %d Responses requests",
			len(session.responses),
			session.requestCount,
		)
	}
	trace := session.trace
	first := *session.trace.FirstRequest
	first.Tools = append([]harnesscodex.ResponsesToolDeclaration(nil), first.Tools...)
	trace.FirstRequest = &first
	trace.Responses = make([]harnesscodex.ResponsesResponseTrace, session.requestCount)
	for sequence := 0; sequence < session.requestCount; sequence++ {
		response, ok := session.responses[sequence]
		if !ok {
			return harnesscodex.ResponsesTrace{}, errors.New("Codex Responses sequence is incomplete")
		}
		trace.Responses[sequence] = cloneResponseTrace(response)
	}
	return trace, nil
}

func (session *ArmSession) partialTrace() (harnesscodex.PartialResponsesTrace, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.fatal != nil {
		return harnesscodex.PartialResponsesTrace{}, session.fatal
	}
	partial := harnesscodex.PartialResponsesTrace{
		SchemaVersion:     harnesscodex.PartialResponsesTraceSchemaVersion,
		Responses:         make([]harnesscodex.ResponsesResponseTrace, 0, len(session.responses)),
		ResponseSequences: make([]int, 0, len(session.responses)),
		RequestCount:      session.requestCount,
	}
	if session.trace.FirstRequest != nil {
		first := *session.trace.FirstRequest
		first.Tools = append([]harnesscodex.ResponsesToolDeclaration(nil), first.Tools...)
		partial.FirstRequest = &first
	}
	for sequence := 0; sequence < session.requestCount; sequence++ {
		response, ok := session.responses[sequence]
		if !ok {
			continue
		}
		partial.ResponseSequences = append(partial.ResponseSequences, sequence)
		partial.Responses = append(partial.Responses, cloneResponseTrace(response))
	}
	return partial, nil
}

func cloneResponseTrace(
	response harnesscodex.ResponsesResponseTrace,
) harnesscodex.ResponsesResponseTrace {
	response.Outputs = append(
		make([]harnesscodex.ResponsesOutputTrace, 0, len(response.Outputs)),
		response.Outputs...,
	)
	if response.Usage != nil {
		usage := *response.Usage
		response.Usage = &usage
	}
	if response.ProviderTotalTokens != nil {
		total := *response.ProviderTotalTokens
		response.ProviderTotalTokens = &total
	}
	connections := response.TLSConnections
	response.TLSConnections = make(
		[]harnesscodex.TLSConnectionTrace,
		len(connections),
	)
	for index, connection := range connections {
		response.TLSConnections[index] = cloneTLSConnectionTrace(connection)
	}
	return response
}

func (lifecycle *Lifecycle) storeAndCompare(
	request genericrunner.ExecutionRequest,
	snapshot armSnapshot,
) error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	paired := lifecycle.pairs[request.Repetition]
	if paired == nil {
		paired = make(map[genericrunner.Arm]armSnapshot, 2)
		lifecycle.pairs[request.Repetition] = paired
	}
	if _, duplicate := paired[request.Arm]; duplicate {
		return errors.New("Codex arm snapshot already exists for this repetition")
	}
	paired[request.Arm] = snapshot
	baseline, hasBaseline := paired[genericrunner.BaselineArm]
	candidate, hasCandidate := paired[genericrunner.CandidateArm]
	if !hasBaseline || !hasCandidate {
		return nil
	}
	delete(lifecycle.pairs, request.Repetition)
	if err := comparePairSnapshots(baseline, candidate); err != nil {
		return fmt.Errorf("paired Codex capture integrity: %w", err)
	}
	return nil
}

var _ genericrunner.ArmSession = (*ArmSession)(nil)

// AllowedConnectTCPPorts exposes only the reserved loopback proxy port needed
// by the contained Codex child. The runner converts this session-bound policy
// into its per-arm network rules; no address or capability is disclosed here.
func (session *ArmSession) AllowedConnectTCPPorts() []uint16 {
	if session == nil || session.lifecycle == nil || session.lifecycle.proxyPort == 0 {
		return nil
	}
	return []uint16{session.lifecycle.proxyPort}
}

// AllowedBindTCPPorts is canonically empty: the Codex child never owns a
// listening socket.
func (*ArmSession) AllowedBindTCPPorts() []uint16 {
	return []uint16{}
}
