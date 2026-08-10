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
// proxy requests, captures the effective configuration and provider request
// state for every terminal process state, compares the pair after the second
// arm, and resets all private runtime directories. Ordinary process failures
// retain their partial provider evidence, but missing or asymmetric capture
// state is an integrity error and can never acquire publication authority.
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
		return nil, errors.New("codex arm request changed between BeginArm and Finish")
	}
	if err := session.startFinalization(sessionFinishing); err != nil {
		return nil, err
	}
	inflightDrained := false
	defer func() { session.finishFinalization(inflightDrained) }()
	if err := session.waitInflight(ctx); err != nil {
		return nil, err
	}
	inflightDrained = true
	configRaw, configCommon, configDelta, configErr := session.captureEffectiveConfig(ctx)
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
		artifacts := make([]harness.Artifact, 0, 2)
		if partialErr == nil {
			artifacts = append(artifacts, harness.Artifact{
				Name:      harnesscodex.PartialResponsesTraceArtifactName,
				MediaType: harnesscodex.PartialResponsesTraceMediaType,
				Data:      append([]byte(nil), traceRaw...),
			})
		}
		if configErr == nil {
			artifacts = append(artifacts, harness.Artifact{
				Name:      harnesscodex.EffectiveConfigArtifactName,
				MediaType: harnesscodex.EffectiveConfigMediaType,
				Data:      append([]byte(nil), configRaw...),
			})
		}
		var pairErr error
		if partialErr == nil && configErr == nil {
			snapshot := armSnapshot{
				trace: harnesscodex.ResponsesTrace{
					SchemaVersion:    harnesscodex.ResponsesTraceSchemaVersion,
					FirstRequest:     partial.FirstRequest,
					Requests:         cloneRequestSnapshots(partial.Requests),
					ResponseAttempts: append([]harnesscodex.ProviderResponseAttemptTrace(nil), partial.ResponseAttempts...),
					Responses:        append([]harnesscodex.ResponsesResponseTrace(nil), partial.Responses...),
					TLSRequired:      partial.TLSRequired,
				},
				config:       append([]byte(nil), configRaw...),
				configCommon: append([]byte(nil), configCommon...),
				configDelta:  append([]byte(nil), configDelta...),
				requestCount: partial.RequestCount,
				ordinary:     true,
				captureError: partial.CaptureErrorSHA256,
			}
			pairErr = session.lifecycle.storeAndCompare(session.request, snapshot)
		}
		resetErr := session.lifecycle.resetLayout(ctx)
		return artifacts, errors.Join(partialErr, configErr, pairErr, resetErr)
	}

	trace, err := session.finalTrace()
	if err != nil {
		partial, partialErr := session.partialTrace()
		partial.CaptureErrorSHA256 = bytesDigest([]byte(err.Error()))
		traceRaw, marshalErr := json.Marshal(partial)
		if marshalErr == nil && (len(traceRaw) == 0 || len(traceRaw) > harnesscodex.MaxResponsesTraceBytes) {
			marshalErr = errors.New("sanitized partial Codex Responses trace exceeds its artifact limit")
		}
		if marshalErr == nil {
			_, marshalErr = harnesscodex.ParsePartialResponsesTrace(traceRaw)
		}
		artifacts := make([]harness.Artifact, 0, 2)
		if partialErr == nil && marshalErr == nil {
			artifacts = append(artifacts, harness.Artifact{
				Name:      harnesscodex.PartialResponsesTraceArtifactName,
				MediaType: harnesscodex.PartialResponsesTraceMediaType,
				Data:      append([]byte(nil), traceRaw...),
			})
		}
		if configErr == nil {
			artifacts = append(artifacts, harness.Artifact{
				Name:      harnesscodex.EffectiveConfigArtifactName,
				MediaType: harnesscodex.EffectiveConfigMediaType,
				Data:      append([]byte(nil), configRaw...),
			})
		}
		var pairErr error
		if partialErr == nil && marshalErr == nil && configErr == nil {
			pairErr = session.lifecycle.storeAndCompare(session.request, armSnapshot{
				trace: harnesscodex.ResponsesTrace{
					SchemaVersion:    harnesscodex.ResponsesTraceSchemaVersion,
					FirstRequest:     partial.FirstRequest,
					Requests:         cloneRequestSnapshots(partial.Requests),
					ResponseAttempts: append([]harnesscodex.ProviderResponseAttemptTrace(nil), partial.ResponseAttempts...),
					Responses:        append([]harnesscodex.ResponsesResponseTrace(nil), partial.Responses...),
					TLSRequired:      partial.TLSRequired,
				},
				config:       append([]byte(nil), configRaw...),
				configCommon: append([]byte(nil), configCommon...),
				configDelta:  append([]byte(nil), configDelta...),
				requestCount: partial.RequestCount,
				captureError: partial.CaptureErrorSHA256,
			})
		}
		return artifacts, errors.Join(
			err, partialErr, marshalErr, configErr, pairErr,
			session.lifecycle.resetLayout(ctx),
		)
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
	if configErr != nil {
		return []harness.Artifact{{
			Name:      harnesscodex.ResponsesTraceArtifactName,
			MediaType: harnesscodex.ResponsesTraceMediaType,
			Data:      append([]byte(nil), traceRaw...),
		}}, errors.Join(configErr, session.lifecycle.resetLayout(ctx))
	}

	snapshot := armSnapshot{
		trace:        parsedTrace,
		config:       append([]byte(nil), configRaw...),
		configCommon: append([]byte(nil), configCommon...),
		configDelta:  append([]byte(nil), configDelta...),
		requestCount: len(parsedTrace.Responses),
	}
	artifacts := []harness.Artifact{
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
	}
	if err := session.lifecycle.storeAndCompare(session.request, snapshot); err != nil {
		return artifacts, errors.Join(err, session.lifecycle.resetLayout(ctx))
	}
	if err := session.lifecycle.resetLayout(ctx); err != nil {
		return artifacts, err
	}
	return artifacts, nil
}

func (session *ArmSession) captureEffectiveConfig(
	ctx context.Context,
) ([]byte, []byte, []byte, error) {
	configRaw, err := session.lifecycle.readEffectiveConfig(ctx)
	if err != nil {
		return nil, nil, nil, err
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
		return nil, nil, nil, err
	}
	return configRaw, configCommon, configDelta, nil
}

func ordinaryTerminalExecution(raw harness.RawExecution) bool {
	return raw.LaunchFailed || raw.TimedOut || raw.Cancelled ||
		raw.StdoutTruncated || raw.StderrTruncated || raw.ExitCode != 0
}

// Abort implements runner.ArmSession. It is idempotent, closes admission,
// waits using the caller's independent cleanup context, and resets only the
// lifecycle's claimed state tree.
func (session *ArmSession) Abort(ctx context.Context) error {
	terminal, err := session.startAbort()
	if err != nil || terminal {
		return err
	}
	return session.completeAbort(ctx)
}

func (session *ArmSession) startAbort() (bool, error) {
	lifecycle := session.lifecycle
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	switch session.state {
	case sessionFinished, sessionAborted:
		return true, nil
	case sessionAborting:
		if lifecycle.finalizing != session {
			return false, errors.New("codex aborting arm is not registered for finalization")
		}
		return false, nil
	case sessionFinishing:
		return false, errors.New("codex arm is currently finishing")
	case sessionActive:
		if lifecycle.active != session {
			return false, errors.New("codex active arm is not registered for admission")
		}
		if lifecycle.finalizing != nil {
			return false, errors.New("codex lifecycle already has a finalizing arm")
		}
		session.state = sessionAborting
		lifecycle.active = nil
		lifecycle.finalizing = session
		return false, nil
	default:
		return false, errors.New("codex arm has an invalid state")
	}
}

func (session *ArmSession) completeAbort(ctx context.Context) error {
	if ctx == nil {
		return errors.New("codex arm abort context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.cleanupPermit:
	}
	defer func() { session.cleanupPermit <- struct{}{} }()

	session.mu.Lock()
	state := session.state
	session.mu.Unlock()
	switch state {
	case sessionFinished, sessionAborted:
		return nil
	case sessionFinishing:
		return errors.New("codex arm is currently finishing")
	case sessionActive:
		return errors.New("codex arm remained active while aborting")
	case sessionAborting:
	default:
		return errors.New("codex arm has an invalid state")
	}
	if err := session.waitInflight(ctx); err != nil {
		return err
	}
	if err := session.lifecycle.resetLayout(ctx); err != nil {
		return err
	}
	session.completeFinalization(sessionAborted)
	return nil
}

func (session *ArmSession) startFinalization(next sessionState) error {
	lifecycle := session.lifecycle
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != sessionActive {
		return errors.New("codex arm session is already consumed")
	}
	if lifecycle.closed {
		return errors.New("codex lifecycle is closed")
	}
	if lifecycle.active != session {
		return errors.New("codex active arm is not registered for admission")
	}
	if lifecycle.finalizing != nil {
		return errors.New("codex lifecycle already has a finalizing arm")
	}
	session.state = next
	lifecycle.active = nil
	lifecycle.finalizing = session
	return nil
}

func (session *ArmSession) completeFinalization(next sessionState) {
	lifecycle := session.lifecycle
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if lifecycle.finalizing == session {
		session.state = next
		lifecycle.finalizing = nil
	}
}

func (session *ArmSession) finishFinalization(inflightDrained bool) {
	if inflightDrained {
		session.completeFinalization(sessionFinished)
	} else {
		lifecycle := session.lifecycle
		lifecycle.mu.Lock()
		session.mu.Lock()
		if lifecycle.finalizing == session && session.state == sessionFinishing {
			session.state = sessionAborting
		}
		session.mu.Unlock()
		lifecycle.mu.Unlock()
	}
	close(session.finishReturned)
}

func (session *ArmSession) waitInflight(ctx context.Context) error {
	if ctx == nil {
		return errors.New("codex arm cleanup context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	session.drainOnce.Do(func() {
		go func() {
			session.inflight.Wait()
			close(session.inflightDrained)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.inflightDrained:
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
		return harnesscodex.ResponsesTrace{}, errors.New("codex arm made no completed Responses request")
	}
	if len(session.responses) != session.requestCount {
		return harnesscodex.ResponsesTrace{}, fmt.Errorf(
			"codex arm completed %d of %d Responses requests",
			len(session.responses),
			session.requestCount,
		)
	}
	if len(session.responseAttempts) != session.requestCount {
		return harnesscodex.ResponsesTrace{}, fmt.Errorf(
			"codex arm captured %d of %d provider response attempts",
			len(session.responseAttempts), session.requestCount,
		)
	}
	trace := session.trace
	trace.TLSRequired = session.lifecycle.production
	first := *session.trace.FirstRequest
	first.Tools = append([]harnesscodex.ResponsesToolDeclaration(nil), first.Tools...)
	first.DynamicFields = cloneDynamicFields(first.DynamicFields)
	trace.FirstRequest = &first
	trace.Requests = cloneRequestSnapshots(session.requests)
	trace.ResponseAttempts = cloneResponseAttempts(session.responseAttempts, session.requestCount)
	trace.Responses = make([]harnesscodex.ResponsesResponseTrace, session.requestCount)
	for sequence := range session.requestCount {
		response, ok := session.responses[sequence]
		if !ok {
			return harnesscodex.ResponsesTrace{}, errors.New("codex Responses sequence is incomplete")
		}
		trace.Responses[sequence] = cloneResponseTrace(response)
	}
	return trace, nil
}

func (session *ArmSession) partialTrace() (harnesscodex.PartialResponsesTrace, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	partial := harnesscodex.PartialResponsesTrace{
		SchemaVersion:     harnesscodex.PartialResponsesTraceSchemaVersion,
		Requests:          cloneRequestSnapshots(session.requests),
		ResponseAttempts:  cloneResponseAttempts(session.responseAttempts, session.requestCount),
		Responses:         make([]harnesscodex.ResponsesResponseTrace, 0, len(session.responses)),
		ResponseSequences: make([]int, 0, len(session.responses)),
		RequestCount:      session.requestCount,
		TLSRequired:       session.lifecycle.production,
	}
	if session.fatal != nil {
		partial.CaptureErrorSHA256 = bytesDigest([]byte(session.fatal.Error()))
	}
	if session.trace.FirstRequest != nil {
		first := *session.trace.FirstRequest
		first.Tools = append([]harnesscodex.ResponsesToolDeclaration(nil), first.Tools...)
		first.DynamicFields = cloneDynamicFields(first.DynamicFields)
		partial.FirstRequest = &first
	}
	for sequence := range session.requestCount {
		response, ok := session.responses[sequence]
		if !ok {
			continue
		}
		partial.ResponseSequences = append(partial.ResponseSequences, sequence)
		partial.Responses = append(partial.Responses, cloneResponseTrace(response))
	}
	return partial, nil
}

func cloneResponseAttempts(
	attempts map[int]harnesscodex.ProviderResponseAttemptTrace,
	requestCount int,
) []harnesscodex.ProviderResponseAttemptTrace {
	clones := make([]harnesscodex.ProviderResponseAttemptTrace, 0, len(attempts))
	for sequence := range requestCount {
		attempt, ok := attempts[sequence]
		if !ok {
			continue
		}
		attempt.TLSConnections = cloneTLSConnectionTraces(attempt.TLSConnections)
		clones = append(clones, attempt)
	}
	return clones
}

func cloneRequestSnapshots(
	requests []harnesscodex.ResponsesRequestSnapshot,
) []harnesscodex.ResponsesRequestSnapshot {
	clones := make([]harnesscodex.ResponsesRequestSnapshot, len(requests))
	for index, request := range requests {
		clones[index] = request
		clones[index].DynamicFields = cloneDynamicFields(request.DynamicFields)
	}
	return clones
}

func cloneResponseTrace(
	response harnesscodex.ResponsesResponseTrace,
) harnesscodex.ResponsesResponseTrace {
	response.Outputs = append(
		make([]harnesscodex.ResponsesOutputTrace, 0, len(response.Outputs)),
		response.Outputs...,
	)
	response.Wire.SSEEvents = append(
		make([]harnesscodex.ResponsesSSEEventTrace, 0, len(response.Wire.SSEEvents)),
		response.Wire.SSEEvents...,
	)
	if response.Wire.CompletedEventSequence != nil {
		sequence := *response.Wire.CompletedEventSequence
		response.Wire.CompletedEventSequence = &sequence
	}
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
		return errors.New("codex arm snapshot already exists for this repetition")
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
