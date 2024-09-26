package discovery

import (
	"context"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/overmindtech/sdp-go"
)

type testHeartbeatClient struct {
	// Requests will be sent to this channel
	Requests chan *connect.Request[sdp.SubmitSourceHeartbeatRequest]
	// Responses should be sent here
	Responses chan *connect.Response[sdp.SubmitSourceHeartbeatResponse]
}

func (t testHeartbeatClient) SubmitSourceHeartbeat(ctx context.Context, req *connect.Request[sdp.SubmitSourceHeartbeatRequest]) (*connect.Response[sdp.SubmitSourceHeartbeatResponse], error) {
	t.Requests <- req
	return <-t.Responses, nil
}

func TestHeartbeats(t *testing.T) {
	name := t.Name()
	u := uuid.New()
	version := "v0.0.0-test"
	engineType := "aws"

	requests := make(chan *connect.Request[sdp.SubmitSourceHeartbeatRequest], 1)
	responses := make(chan *connect.Response[sdp.SubmitSourceHeartbeatResponse], 1)

	e, _ := NewEngine()
	e.Name = name
	e.UUID = u
	e.Version = version
	e.Type = engineType
	e.Managed = sdp.SourceManaged_LOCAL
	e.HeartbeatOptions = &HeartbeatOptions{
		ManagementClient: testHeartbeatClient{
			Requests:  requests,
			Responses: responses,
		},
	}

	e.AddSources(
		&TestSource{
			ReturnScopes: []string{"test"},
			ReturnType:   "test-type",
		},
		&TestSource{
			ReturnScopes: []string{"test"},
			ReturnType:   "test-type2",
		},
		&TestSource{
			ReturnScopes: []string{"test2"},
			ReturnType:   "test-type",
		},
	)

	t.Run("sendHeartbeat when healthy", func(t *testing.T) {
		e.HeartbeatOptions.HealthCheck = func() error {
			return nil
		}
		responses <- &connect.Response[sdp.SubmitSourceHeartbeatResponse]{
			Msg: &sdp.SubmitSourceHeartbeatResponse{},
		}

		err := e.SendHeartbeat(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		req := <-requests

		if reqUUID, err := uuid.FromBytes(req.Msg.GetUUID()); err == nil {
			if reqUUID != u {
				t.Errorf("expected uuid %v, got %v", u, reqUUID)
			}
		} else {
			t.Errorf("error parsing uuid: %v", err)
		}

		if req.Msg.GetVersion() != version {
			t.Errorf("expected version %v, got %v", version, req.Msg.GetVersion())
		}

		if req.Msg.GetName() != name {
			t.Errorf("expected name %v, got %v", name, req.Msg.GetName())
		}

		if req.Msg.GetType() != engineType {
			t.Errorf("expected type %v, got %v", engineType, req.Msg.GetType())
		}

		if req.Msg.GetManaged() != sdp.SourceManaged_LOCAL {
			t.Errorf("expected managed %v, got %v", sdp.SourceManaged_LOCAL, req.Msg.GetManaged())
		}

		if req.Msg.GetError() != "" {
			t.Errorf("expected no error, got %v", req.Msg.GetError())
		}

		reqAvailableScopes := req.Msg.GetAvailableScopes()

		if len(reqAvailableScopes) != 2 {
			t.Errorf("expected 2 scopes, got %v", len(reqAvailableScopes))
		}

		if !slices.Contains(reqAvailableScopes, "test") {
			t.Errorf("expected scope 'test' to be present in the response")
		}

		if !slices.Contains(reqAvailableScopes, "test2") {
			t.Errorf("expected scope 'test2' to be present in the response")
		}

		reqAvailableTypes := req.Msg.GetAvailableTypes()

		if len(reqAvailableTypes) != 2 {
			t.Errorf("expected 2 types, got %v", len(reqAvailableTypes))
		}

		if !slices.Contains(reqAvailableTypes, "test-type") {
			t.Errorf("expected type 'test-type' to be present in the response")
		}

		if !slices.Contains(reqAvailableTypes, "test-type2") {
			t.Errorf("expected type 'test-type2' to be present in the response")
		}
	})

	t.Run("sendHeartbeat when unhealthy", func(t *testing.T) {
		e.HeartbeatOptions.HealthCheck = func() error {
			return ErrNoHealthcheckDefined
		}

		responses <- &connect.Response[sdp.SubmitSourceHeartbeatResponse]{
			Msg: &sdp.SubmitSourceHeartbeatResponse{},
		}

		err := e.SendHeartbeat(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		req := <-requests

		if req.Msg.GetError() != ErrNoHealthcheckDefined.Error() {
			t.Errorf("expected error %v, got %v", ErrNoHealthcheckDefined, req.Msg.GetError())
		}
	})

	t.Run("startSendingHeartbeats", func(t *testing.T) {
		e.HeartbeatOptions.Frequency = time.Millisecond * 250
		e.HeartbeatOptions.HealthCheck = func() error {
			return nil
		}

		ctx, cancel := context.WithCancel(context.Background())

		e.StartSendingHeartbeats(ctx)

		start := time.Now()
		// Get one
		responses <- &connect.Response[sdp.SubmitSourceHeartbeatResponse]{
			Msg: &sdp.SubmitSourceHeartbeatResponse{},
		}
		<-requests

		// Get two
		responses <- &connect.Response[sdp.SubmitSourceHeartbeatResponse]{
			Msg: &sdp.SubmitSourceHeartbeatResponse{},
		}
		<-requests

		cancel()

		// Make sure that took the expected amount of time
		if elapsed := time.Since(start); elapsed < time.Millisecond*500 {
			t.Errorf("expected to take at least 500ms, took %v", elapsed)
		}

		if elapsed := time.Since(start); elapsed > time.Millisecond*750 {
			t.Errorf("expected to take at most 750ms, took %v", elapsed)
		}
	})
}
