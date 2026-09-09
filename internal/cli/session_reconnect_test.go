package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"github.com/kelos-dev/kelos/internal/sessionsuspend"
)

func TestSessionTerminalReconnectsToReplacementPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	output := newSessionTestOutput("agent › recovered")
	var stderr bytes.Buffer
	firstConnected := make(chan struct{})
	secondConnected := make(chan struct{})
	secondMessage := make(chan struct{})
	firstRequestID := make(chan string, 1)
	var sessionReads atomic.Int32
	dependencies := sessionReconnectDependencies{
		getSession: func(context.Context, string, string) (*kelos.Session, error) {
			switch sessionReads.Add(1) {
			case 1:
				return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod-1"}}, nil
			case 2:
				return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseFailed, Message: "Session runtime is restarting"}}, nil
			default:
				return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod-2"}}, nil
			}
		},
		openStream: func(_ context.Context, _ string, podName string, _ io.Writer) (*sessionPodStream, error) {
			switch podName {
			case "session-pod-1":
				return fakeSessionPodStream(t, func(decoder *json.Decoder, encoder *json.Encoder) {
					var subscribe sessionruntime.ClientRequest
					if err := decoder.Decode(&subscribe); err != nil {
						t.Error(err)
						return
					}
					if subscribe.Type != "subscribe" || subscribe.Since != 0 || subscribe.JournalID != "" || !subscribe.HistoryBounds || subscribe.HistoryItems != sessionruntime.DefaultHistoryItemLimit || subscribe.HistoryBytes != sessionruntime.DefaultHistoryByteLimit {
						t.Errorf("first subscribe = %#v", subscribe)
					}
					_ = encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, JournalID: "journal-1", LastEventID: 10})
					_ = encoder.Encode(sessionruntime.Event{ID: 1, Type: sessionruntime.EventTurnStarted, TurnID: "turn-1", Status: "running"})
					_ = encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
					close(firstConnected)
					var request sessionruntime.ClientRequest
					if err := decoder.Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if request.Type != "message" || request.Text != "before" {
						t.Errorf("first message = %#v", request)
					}
					if request.RequestID == "" {
						t.Error("first message has no request ID")
					}
					firstRequestID <- request.RequestID
				}), nil
			case "session-pod-2":
				return fakeSessionPodStream(t, func(decoder *json.Decoder, encoder *json.Encoder) {
					var subscribe sessionruntime.ClientRequest
					if err := decoder.Decode(&subscribe); err != nil {
						t.Error(err)
						return
					}
					if subscribe.Type != "subscribe" || subscribe.Since != 10 || subscribe.JournalID != "journal-1" || !subscribe.HistoryBounds || subscribe.HistoryItems != sessionruntime.DefaultHistoryItemLimit || subscribe.HistoryBytes != sessionruntime.DefaultHistoryByteLimit {
						t.Errorf("replacement subscribe = %#v", subscribe)
					}
					_ = encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryStart, JournalID: "journal-2", Reset: true})
					_ = encoder.Encode(sessionruntime.Event{ID: 1, Type: sessionruntime.EventRuntimeRecovered, Text: "Session runtime restarted"})
					_ = encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd})
					close(secondConnected)
					var request sessionruntime.ClientRequest
					if err := decoder.Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if request.Type != "message" || request.Text != "after" {
						t.Errorf("replacement message = %#v", request)
					}
					if request.RequestID == "" || request.RequestID == <-firstRequestID {
						t.Errorf("replacement request ID = %q", request.RequestID)
					}
					_ = encoder.Encode(sessionruntime.Event{ID: 2, Type: sessionruntime.EventUserMessage, RequestID: request.RequestID, TurnID: "turn-2", Text: "after"})
					_ = encoder.Encode(sessionruntime.Event{ID: 3, Type: sessionruntime.EventAssistantMessage, TurnID: "turn-2", Text: "recovered"})
					_ = encoder.Encode(sessionruntime.Event{ID: 4, Type: sessionruntime.EventTurnCompleted, TurnID: "turn-2", Status: "completed"})
					close(secondMessage)
					<-ctx.Done()
				}), nil
			default:
				t.Fatalf("unexpected Pod %q", podName)
				return nil, nil
			}
		},
		runTerminal: runSessionTerminal,
	}
	done := make(chan error, 1)
	go func() {
		done <- connectSessionWithDependencies(ctx, "default", "chat", input, output, &stderr, false, dependencies)
	}()
	select {
	case <-firstConnected:
	case <-ctx.Done():
		t.Fatal("first Session stream did not connect")
	}
	_, _ = io.WriteString(inputWriter, "before\n")
	select {
	case <-secondConnected:
	case <-ctx.Done():
		t.Fatal("replacement Session stream did not connect")
	}
	_, _ = io.WriteString(inputWriter, "after\n")
	select {
	case <-secondMessage:
	case <-ctx.Done():
		t.Fatal("replacement Session did not receive input")
	}
	select {
	case <-output.found:
	case <-ctx.Done():
		t.Fatal("terminal did not render replacement response")
	}
	_, _ = io.WriteString(inputWriter, "/quit\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("terminal did not exit")
	}
	if got := output.String(); !strings.Contains(got, "Session runtime restarted") || !strings.Contains(got, "agent › recovered") {
		t.Fatalf("terminal output = %q", got)
	}
	if got := stderr.String(); !strings.Contains(got, "connection lost") || !strings.Contains(got, "Waiting for Session \"chat\" to recover") || !strings.Contains(got, "Reconnected") || !strings.Contains(got, "delivery was not confirmed") {
		t.Fatalf("terminal stderr = %q", got)
	}
}

type sessionTestOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	target string
	found  chan struct{}
	once   sync.Once
}

func newSessionTestOutput(target string) *sessionTestOutput {
	return &sessionTestOutput{target: target, found: make(chan struct{})}
}

func (w *sessionTestOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written, err := w.buffer.Write(data)
	if strings.Contains(w.buffer.String(), w.target) {
		w.once.Do(func() { close(w.found) })
	}
	return written, err
}

func (w *sessionTestOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestSessionConnectionWaitsForTerminalCleanup(t *testing.T) {
	terminalExited := make(chan struct{})
	dependencies := sessionReconnectDependencies{
		getSession: func(context.Context, string, string) (*kelos.Session, error) {
			return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseFailed, Message: "runtime failed"}}, nil
		},
		openStream: func(context.Context, string, string, io.Writer) (*sessionPodStream, error) {
			t.Fatal("openStream called for failed Session")
			return nil, nil
		},
		runTerminal: func(ctx context.Context, _ io.Reader, _ io.Writer, _ io.Reader, _ io.Writer, _ bool) error {
			<-ctx.Done()
			close(terminalExited)
			return ctx.Err()
		},
	}

	err := connectSessionWithDependencies(t.Context(), "default", "chat", strings.NewReader(""), io.Discard, io.Discard, false, dependencies)
	if err == nil || !strings.Contains(err.Error(), "runtime failed") {
		t.Fatalf("connectSessionWithDependencies() error = %v, want Session failure", err)
	}
	select {
	case <-terminalExited:
	default:
		t.Fatal("connectSessionWithDependencies() returned before terminal cleanup")
	}
}

func TestWaitForReadySessionPreservesNotFoundError(t *testing.T) {
	_, err := waitForReadySession(
		t.Context(),
		"default",
		"chat",
		io.Discard,
		func(context.Context, string, string) (*kelos.Session, error) {
			return nil, apierrors.NewNotFound(schema.GroupResource{Group: kelos.GroupVersion.Group, Resource: "sessions"}, "chat")
		},
		nil,
		make(chan error),
		false,
	)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("waitForReadySession() error = %v, want Kubernetes NotFound error", err)
	}
}

func TestWaitForReadySessionReportsSuspension(t *testing.T) {
	var stderr bytes.Buffer
	var reads atomic.Int32
	session, err := waitForReadySession(
		t.Context(),
		"default",
		"chat",
		&stderr,
		func(context.Context, string, string) (*kelos.Session, error) {
			if reads.Add(1) == 1 {
				return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseSuspended}}, nil
			}
			return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod"}}, nil
		},
		nil,
		make(chan error),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status.PodName != "session-pod" {
		t.Fatalf("ready Session Pod = %q, want session-pod", session.Status.PodName)
	}
	if !strings.Contains(stderr.String(), `Waiting for Session "chat" to resume`) {
		t.Fatalf("Session diagnostics = %q", stderr.String())
	}
}

func TestWaitForReadySessionRequestsIdleResume(t *testing.T) {
	var stderr bytes.Buffer
	var reads atomic.Int32
	var resumeRequests atomic.Int32
	session, err := waitForReadySession(
		t.Context(),
		"default",
		"chat",
		&stderr,
		func(context.Context, string, string) (*kelos.Session, error) {
			if reads.Add(1) == 1 {
				return &kelos.Session{Status: kelos.SessionStatus{
					Phase: kelos.SessionPhaseSuspended,
					Conditions: []metav1.Condition{{
						Type:   kelos.SessionConditionReady,
						Status: metav1.ConditionFalse,
						Reason: sessionsuspend.IdlePolicyReason,
					}},
				}}, nil
			}
			return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod"}}, nil
		},
		func(_ context.Context, namespace, name string) error {
			if namespace != "default" || name != "chat" {
				t.Fatalf("resume Session = %s/%s, want default/chat", namespace, name)
			}
			resumeRequests.Add(1)
			return nil
		},
		make(chan error),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status.PodName != "session-pod" {
		t.Fatalf("ready Session Pod = %q, want session-pod", session.Status.PodName)
	}
	if resumeRequests.Load() != 1 {
		t.Fatalf("resume requests = %d, want 1", resumeRequests.Load())
	}
	if !strings.Contains(stderr.String(), `Resuming idle Session "chat"`) {
		t.Fatalf("Session diagnostics = %q", stderr.String())
	}
}

func TestWaitForReadySessionReturnsWhenIdleResumeExpires(t *testing.T) {
	var reads atomic.Int32
	_, err := waitForReadySession(
		t.Context(),
		"default",
		"chat",
		io.Discard,
		func(context.Context, string, string) (*kelos.Session, error) {
			return &kelos.Session{Status: kelos.SessionStatus{
				Phase: kelos.SessionPhaseSuspended,
				Conditions: []metav1.Condition{{
					Type:   kelos.SessionConditionReady,
					Status: metav1.ConditionFalse,
					Reason: sessionsuspend.IdlePolicyReason,
				}},
			}}, nil
		},
		func(context.Context, string, string) error {
			reads.Add(1)
			return nil
		},
		make(chan error),
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "resume request expired") {
		t.Fatalf("waitForReadySession() error = %v, want resume expiry", err)
	}
	if reads.Load() != 1 {
		t.Fatalf("resume requests = %d, want 1", reads.Load())
	}
}

func TestWaitForReadySessionDoesNotResumeIdleSessionAfterConnection(t *testing.T) {
	var stderr bytes.Buffer
	var resumeRequests atomic.Int32
	_, err := waitForReadySession(
		t.Context(),
		"default",
		"chat",
		&stderr,
		func(context.Context, string, string) (*kelos.Session, error) {
			return &kelos.Session{Status: kelos.SessionStatus{
				Phase: kelos.SessionPhaseSuspended,
				Conditions: []metav1.Condition{{
					Type:   kelos.SessionConditionReady,
					Status: metav1.ConditionFalse,
					Reason: sessionsuspend.IdlePolicyReason,
				}},
			}}, nil
		},
		func(context.Context, string, string) error {
			resumeRequests.Add(1)
			return nil
		},
		make(chan error),
		true,
	)
	if !errors.Is(err, errSessionIdleSuspended) {
		t.Fatalf("waitForReadySession() error = %v, want %v", err, errSessionIdleSuspended)
	}
	if resumeRequests.Load() != 0 {
		t.Fatalf("resume requests = %d, want 0", resumeRequests.Load())
	}
	if !strings.Contains(stderr.String(), `Session "chat" suspended after becoming idle`) {
		t.Fatalf("Session diagnostics = %q", stderr.String())
	}
}

func TestSessionConnectionReturnsTerminalDeliveryError(t *testing.T) {
	runtimeEventSent := make(chan struct{})
	terminalErr := errors.New("terminal output failed")
	dependencies := sessionReconnectDependencies{
		getSession: func(context.Context, string, string) (*kelos.Session, error) {
			return &kelos.Session{Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod"}}, nil
		},
		openStream: func(context.Context, string, string, io.Writer) (*sessionPodStream, error) {
			return fakeSessionPodStream(t, func(decoder *json.Decoder, encoder *json.Encoder) {
				var subscribe sessionruntime.ClientRequest
				if err := decoder.Decode(&subscribe); err != nil {
					t.Error(err)
					return
				}
				if err := encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd}); err != nil {
					t.Error(err)
					return
				}
				close(runtimeEventSent)
			}), nil
		},
		runTerminal: func(context.Context, io.Reader, io.Writer, io.Reader, io.Writer, bool) error {
			<-runtimeEventSent
			return terminalErr
		},
	}

	err := connectSessionWithDependencies(t.Context(), "default", "chat", strings.NewReader(""), io.Discard, io.Discard, false, dependencies)
	if !errors.Is(err, terminalErr) {
		t.Fatalf("connectSessionWithDependencies() error = %v, want %v", err, terminalErr)
	}
}

func TestSessionConnectionAcknowledgesIdleResumeAfterHistory(t *testing.T) {
	acknowledged := make(chan struct{})
	dependencies := sessionReconnectDependencies{
		getSession: func(context.Context, string, string) (*kelos.Session, error) {
			return &kelos.Session{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					sessionsuspend.ResumeRequestAnnotation: "resume-request",
				}},
				Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "session-pod"},
			}, nil
		},
		acknowledgeResume: func(_ context.Context, namespace, name, requestValue string) error {
			if namespace != "default" || name != "chat" {
				t.Fatalf("acknowledged Session = %s/%s, want default/chat", namespace, name)
			}
			if requestValue != "resume-request" {
				t.Fatalf("resume request value = %q, want resume-request", requestValue)
			}
			close(acknowledged)
			return nil
		},
		openStream: func(context.Context, string, string, io.Writer) (*sessionPodStream, error) {
			return fakeSessionPodStream(t, func(decoder *json.Decoder, encoder *json.Encoder) {
				var subscribe sessionruntime.ClientRequest
				if err := decoder.Decode(&subscribe); err != nil {
					t.Error(err)
					return
				}
				if err := encoder.Encode(sessionruntime.Event{Type: sessionruntime.EventHistoryEnd}); err != nil {
					t.Error(err)
				}
			}), nil
		},
		runTerminal: func(context.Context, io.Reader, io.Writer, io.Reader, io.Writer, bool) error {
			<-acknowledged
			return nil
		},
	}

	if err := connectSessionWithDependencies(t.Context(), "default", "chat", strings.NewReader(""), io.Discard, io.Discard, false, dependencies); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTerminalDiagnosticWriterEmitsCompleteLines(t *testing.T) {
	var events bytes.Buffer
	writer := &sessionTerminalDiagnosticWriter{
		events: newSessionTerminalEventSink(t.Context(), &events),
	}
	for _, data := range []string{"first\r\nsec", "ond\nthird"} {
		if _, err := io.WriteString(writer, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(&events)
	for _, want := range []string{"first", "second", "third"} {
		var event sessionruntime.Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type != sessionTerminalEventDiagnostic || event.Text != want {
			t.Fatalf("diagnostic event = %#v, want text %q", event, want)
		}
	}
}

func TestReportSessionTerminalDiagnosticEmitsStatus(t *testing.T) {
	var events bytes.Buffer
	writer := &sessionTerminalDiagnosticWriter{
		events: newSessionTerminalEventSink(t.Context(), &events),
	}

	reportSessionTerminalDiagnostic(writer, sessionTerminalStatusReconnecting, "Waiting for Session %q to recover", "chat")

	var event sessionruntime.Event
	if err := json.NewDecoder(&events).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != sessionTerminalEventDiagnostic ||
		event.Status != sessionTerminalStatusReconnecting ||
		event.Text != `Waiting for Session "chat" to recover` {
		t.Fatalf("diagnostic event = %#v", event)
	}
}

func TestReportSessionTerminalDiagnosticPreservesRedirectedTextAndEmitsStatus(t *testing.T) {
	var events bytes.Buffer
	var diagnostics bytes.Buffer
	writer := &sessionTerminalDiagnosticWriter{
		events: newSessionTerminalEventSink(t.Context(), &events),
		output: &diagnostics,
	}

	reportSessionTerminalDiagnostic(writer, sessionTerminalStatusReconnecting, "Waiting for Session %q to recover", "chat")

	if got := diagnostics.String(); got != "Waiting for Session \"chat\" to recover\n" {
		t.Fatalf("redirected diagnostics = %q", got)
	}
	var event sessionruntime.Event
	if err := json.NewDecoder(&events).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != sessionTerminalEventDiagnostic ||
		event.Status != sessionTerminalStatusReconnecting ||
		event.Text != "" {
		t.Fatalf("diagnostic event = %#v, want status-only reconnect event", event)
	}
}

func TestSessionTerminalDiagnosticWriterForwardsRedirectedText(t *testing.T) {
	var events bytes.Buffer
	var diagnostics bytes.Buffer
	writer := &sessionTerminalDiagnosticWriter{
		events: newSessionTerminalEventSink(t.Context(), &events),
		output: &diagnostics,
	}

	if _, err := io.WriteString(writer, "stream warning\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}

	if got := diagnostics.String(); got != "stream warning\n" {
		t.Fatalf("redirected diagnostics = %q", got)
	}
	if events.Len() != 0 {
		t.Fatalf("redirected stream diagnostic also reached TUI: %q", events.String())
	}
}

func TestSessionTerminalEventSinkStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	eventReader, eventWriter := io.Pipe()
	defer eventReader.Close()
	defer eventWriter.Close()
	sink := newSessionTerminalEventSink(ctx, eventWriter)
	done := make(chan error, 1)
	go func() {
		done <- sink.send(sessionruntime.Event{Type: sessionTerminalEventDiagnostic, Text: "waiting"})
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("send() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("send() did not stop after cancellation")
	}
}

func fakeSessionPodStream(t *testing.T, handler func(*json.Decoder, *json.Encoder)) *sessionPodStream {
	t.Helper()
	runtimeRequests, clientRequests := io.Pipe()
	clientEvents, runtimeEvents := io.Pipe()
	streamCtx, cancel := context.WithCancel(t.Context())
	go func() {
		handler(json.NewDecoder(runtimeRequests), json.NewEncoder(runtimeEvents))
		_ = runtimeEvents.Close()
		_ = runtimeRequests.Close()
	}()
	go func() {
		<-streamCtx.Done()
		_ = runtimeRequests.Close()
		_ = runtimeEvents.Close()
	}()
	return &sessionPodStream{requests: clientRequests, events: clientEvents, cancel: cancel}
}
