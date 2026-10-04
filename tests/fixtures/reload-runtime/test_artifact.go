package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"

	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/presentation"
)

type testArtifactReceiver struct {
	mutex      sync.Mutex
	bytes      int64
	digest     string
	metadata   string
	invocation models.ArtifactInvocation
	calls      atomic.Int64
	cancelled  atomic.Bool
	started    chan struct{}
}

func (receiver *testArtifactReceiver) AcceptArtifact(ctx context.Context, input presentation.ArtifactInput) (presentation.ArtifactResponse, error) {
	receiver.calls.Add(1)
	var metadata struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(input.Metadata, &metadata)
	if metadata.Mode == "early" {
		_, _ = io.CopyN(io.Discard, input.Body, 1)
		return acceptedArtifactResponse(), nil
	}
	if metadata.Mode == "wait-cancel" {
		if receiver.started != nil {
			select {
			case receiver.started <- struct{}{}:
			default:
			}
		}
		_, err := io.Copy(io.Discard, input.Body)
		if ctx.Err() != nil {
			receiver.cancelled.Store(true)
			return presentation.ArtifactResponse{}, ctx.Err()
		}
		return presentation.ArtifactResponse{}, err
	}
	hasher := sha256.New()
	count, err := io.Copy(hasher, input.Body)
	if err != nil {
		return presentation.ArtifactResponse{}, err
	}
	receiver.mutex.Lock()
	receiver.bytes = count
	receiver.digest = "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	receiver.metadata = string(input.Metadata)
	receiver.invocation = input.Invocation
	receiver.mutex.Unlock()
	return acceptedArtifactResponse(), nil
}

func acceptedArtifactResponse() presentation.ArtifactResponse {
	body, _ := json.Marshal(map[string]any{"version": 1, "operationId": "fixture-operation", "state": "accepted"})
	return presentation.ArtifactResponse{StatusCode: 202, Body: body}
}

func (receiver *testArtifactReceiver) snapshot() (int64, string, string, models.ArtifactInvocation) {
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()
	return receiver.bytes, receiver.digest, receiver.metadata, receiver.invocation
}

type repeatedByteReader struct{ remaining int64 }

func (reader *repeatedByteReader) Read(buffer []byte) (int, error) {
	if reader.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	for index := range buffer {
		buffer[index] = 'a'
	}
	reader.remaining -= int64(len(buffer))
	return len(buffer), nil
}
