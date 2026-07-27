package sessionupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

const (
	RequestAnnotation     = "kelos.dev/session-runtime-update-request"
	ForceUpdateAnnotation = "kelos.dev/force-session-runtime-update"
)

// Operation identifies why a Session Pod must drain.
type Operation string

const (
	OperationRuntimeUpdate Operation = "RuntimeUpdate"
	OperationIdleSuspend   Operation = "IdleSuspend"
)

// Request asks one specific Session Pod to stop accepting turns and drain.
type Request struct {
	ID        string    `json:"id"`
	PodUID    types.UID `json:"podUID"`
	Operation Operation `json:"operation"`
}

// NewRequest returns a stable request for one Pod and desired StatefulSet revision.
func NewRequest(podUID types.UID, revision string) Request {
	sum := sha256.Sum256([]byte(string(podUID) + "\x00" + revision))
	return Request{ID: hex.EncodeToString(sum[:16]), PodUID: podUID, Operation: OperationRuntimeUpdate}
}

// NewIdleSuspendRequest returns a stable idle-suspension drain request.
func NewIdleSuspendRequest(podUID types.UID, deadline time.Time) Request {
	sum := sha256.Sum256([]byte(string(podUID) + "\x00" + string(OperationIdleSuspend) + "\x00" + deadline.UTC().Format(time.RFC3339Nano)))
	return Request{ID: hex.EncodeToString(sum[:16]), PodUID: podUID, Operation: OperationIdleSuspend}
}

// Encode serializes a request for storage in a Session annotation.
func Encode(request Request) (string, error) {
	value, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encoding Session runtime update request: %w", err)
	}
	return string(value), nil
}

// Decode parses a request stored in a Session annotation.
func Decode(value string) (Request, error) {
	var request Request
	if err := json.Unmarshal([]byte(value), &request); err != nil {
		return Request{}, fmt.Errorf("decoding Session runtime update request: %w", err)
	}
	if request.ID == "" || request.PodUID == "" {
		return Request{}, fmt.Errorf("decoding Session runtime update request: id and podUID must be set")
	}
	if request.Operation == "" {
		request.Operation = OperationRuntimeUpdate
	}
	if request.Operation != OperationRuntimeUpdate && request.Operation != OperationIdleSuspend {
		return Request{}, fmt.Errorf("decoding Session runtime update request: unsupported operation %q", request.Operation)
	}
	return request, nil
}
