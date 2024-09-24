package discovery

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/overmindtech/sdp-go"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/durationpb"
)

const DefaultHeartbeatFrequency = 5 * time.Minute

var ErrNoHealthcheckDefined = errors.New("no healthcheck defined")

// HeartbeatSender sends a heartbeat to the management API
func (e *Engine) sendHeartbeat(ctx context.Context) error {
	if e.HeartbeatOptions.HealthCheck == nil {
		return ErrNoHealthcheckDefined
	}

	healthCheckError := e.HeartbeatOptions.HealthCheck()

	var heartbeatError *string

	if healthCheckError != nil {
		heartbeatError = new(string)
		*heartbeatError = healthCheckError.Error()
	}

	var engineUUID []byte

	if e.UUID != uuid.Nil {
		engineUUID = e.UUID[:]
	}

	// Get available types and scopes
	availableTypesMap := make(map[string]bool)
	availableScopesMap := make(map[string]bool)
	for _, source := range e.sh.VisibleSources() {
		availableTypesMap[source.Type()] = true
		for _, scope := range source.Scopes() {
			availableScopesMap[scope] = true
		}
	}

	// Extract slices from maps
	availableTypes := make([]string, 0)
	availableScopes := make([]string, 0)
	for t := range availableTypesMap {
		availableTypes = append(availableTypes, t)
	}
	for s := range availableScopesMap {
		availableScopes = append(availableScopes, s)
	}

	// Calculate the duration for the next heartbeat, based on the current
	// frequency x2.5 to give us some leeway
	nextHeartbeat := time.Duration(float64(e.HeartbeatOptions.Frequency) * 2.5)

	_, err := e.HeartbeatOptions.ManagementClient.SubmitSourceHeartbeat(ctx, &connect.Request[sdp.SubmitSourceHeartbeatRequest]{
		Msg: &sdp.SubmitSourceHeartbeatRequest{
			UUID:             engineUUID,
			Version:          e.Version,
			Name:             e.Name,
			Type:             e.Type,
			AvailableTypes:   availableTypes,
			AvailableScopes:  availableScopes,
			Managed:          e.Managed,
			Error:            heartbeatError,
			NextHeartbeatMax: durationpb.New(nextHeartbeat),
		},
	})

	return err
}

// Starts sending heartbeats at the specified frequency. This function will block
// until the context is cancelled.
func (e *Engine) startSendingHeartbeats(ctx context.Context) {
	if e.HeartbeatOptions == nil || e.HeartbeatOptions.Frequency == 0 {
		return
	}

	ticker := time.NewTicker(e.HeartbeatOptions.Frequency)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := e.sendHeartbeat(ctx)
			if err != nil {
				log.WithError(err).Error("Failed to send heartbeat")
			}
		}
	}
}
