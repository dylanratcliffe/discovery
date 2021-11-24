package discovery

import (
	"context"

	"github.com/google/uuid"
	"github.com/overmindtech/sdp-go"
	log "github.com/sirupsen/logrus"
)

// NewItemRequestHandler Returns a function whose job is to handle a single
// request. This includes responses, linking etc.
func (e *Engine) ItemRequestHandler(itemRequest *sdp.ItemRequest) {
	if len(e.FilterSources(itemRequest.Type, itemRequest.Context)) == 0 {
		// If we don't have any relevant sources, exit
		return
	}

	// Respond saying we've got it
	responder := sdp.ResponseSender{
		ResponseSubject: itemRequest.ResponseSubject,
	}

	var pub sdp.EncodedPublisher

	if e.IsNATSConnected() {
		pub = e.natsConnection
	} else {
		pub = NilPublisher{}
	}

	responder.Start(
		pub,
		e.Name,
	)

	log.WithFields(log.Fields{
		"type":      itemRequest.Type,
		"method":    itemRequest.Method,
		"query":     itemRequest.Query,
		"linkDepth": itemRequest.LinkDepth,
		"context":   itemRequest.Context,
	}).Info("Received request")

	requestTracker := RequestTracker{
		Request: itemRequest,
		Engine:  e,
	}

	if u, err := uuid.FromBytes(itemRequest.UUID); err == nil {
		e.TrackRequest(u, &requestTracker)
	}

	_, err := requestTracker.Execute()

	// If all failed then return an error
	if err != nil {
		if ire, ok := err.(*sdp.ItemRequestError); ok {
			responder.Error(ire)
		} else {
			switch err {
			case context.Canceled:
				responder.Cancel()
			default:
				ire = &sdp.ItemRequestError{
					ErrorType:   sdp.ItemRequestError_OTHER,
					ErrorString: err.Error(),
					Context:     itemRequest.Context,
				}

				responder.Error(ire)
			}
		}

		logEntry := log.WithFields(log.Fields{
			"errorType":        "OTHER",
			"errorString":      err.Error(),
			"requestType":      itemRequest.Type,
			"requestMethod":    itemRequest.Method,
			"requestQuery":     itemRequest.Query,
			"requestLinkDepth": itemRequest.LinkDepth,
			"requestContext":   itemRequest.Context,
		})

		if ire, ok := err.(*sdp.ItemRequestError); ok && ire.ErrorType == sdp.ItemRequestError_OTHER {
			logEntry.Error("Request ended with unknown error")
		} else {
			logEntry.Info("Request ended with error")
		}
	} else {
		responder.Done()

		log.WithFields(log.Fields{
			"type":      itemRequest.Type,
			"method":    itemRequest.Method,
			"query":     itemRequest.Query,
			"linkDepth": itemRequest.LinkDepth,
			"context":   itemRequest.Context,
		}).Info("Request complete")
	}
}

// ExecuteRequest Executes a single request and returns the results without any
// linking
func (e *Engine) ExecuteRequest(ctx context.Context, req *sdp.ItemRequest) ([]*sdp.Item, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var requestItem *sdp.Item
	var requestError error

	requestItems := make([]*sdp.Item, 0)

	// TODO: Thread safety

	// Make the request of all sources
	switch req.GetMethod() {
	case sdp.RequestMethod_GET:
		requestItem, requestError = e.Get(ctx, req)
		requestItems = append(requestItems, requestItem)
	case sdp.RequestMethod_FIND:
		requestItems, requestError = e.Find(ctx, req)
	case sdp.RequestMethod_SEARCH:
		requestItems, requestError = e.Search(ctx, req)
	}

	// If there was an error in the request then simply return
	if requestError != nil {
		return nil, sdp.NewItemRequestError(requestError)
	}

	for _, i := range requestItems {
		// If the main request had a linkDepth of great than zero it means we
		// need to keep linking, this means that we need to pass down all of the
		// subject info along with the number of remaining links. If the link
		// depth is zero then we just pass then back in their normal form as we
		// won't be executing them
		if req.GetLinkDepth() > 0 {
			for _, lir := range i.LinkedItemRequests {
				lir.LinkDepth = req.LinkDepth - 1
				lir.ItemSubject = req.ItemSubject
				lir.ResponseSubject = req.ResponseSubject
				lir.IgnoreCache = req.IgnoreCache
				lir.Timeout = req.Timeout
				lir.UUID = req.UUID
			}
		}

		// Assign the item request
		if i.Metadata != nil {
			i.Metadata.SourceRequest = req
		}
	}

	return requestItems, requestError
}

// ExpandRequest Expands requests with wildcards to no longer contain wildcards.
// Meaning that if we support 5 types, and a request comes in with a wildcard
// type, this function will expand that request into 5 requests, one for each
// type.
//
// The same goes for contexts, if we have a request with a wildcard context, and
// a single source that supports 5 contexts, we will end up with 5 requests. The
// exception to this is if we have a source that supports all contexts, but is
// unable to list them. In this case there will still be some requests with
// wildcard contexts as they can't be expanded
func (e *Engine) ExpandRequest(request *sdp.ItemRequest) []*sdp.ItemRequest {
	// Filter to just sources that are capable of responding
	relevantSources := e.FilterSources(request.Type, request.Context)

	requests := make([]*sdp.ItemRequest, 0)

	for _, src := range relevantSources {
		for _, ctx := range src.Contexts() {
			// Create a new request if:
			//
			// * The source supports all contexts, or
			// * The request context is a wildcard, or
			// * The request context matches source context
			if IsWildcard(ctx) || IsWildcard(request.Context) || ctx == request.Context {
				requests = append(requests, &sdp.ItemRequest{
					Type:            src.Type(),
					Method:          request.Method,
					Query:           request.Query,
					Context:         ctx,
					ItemSubject:     request.ItemSubject,
					ResponseSubject: request.ResponseSubject,
					LinkDepth:       request.LinkDepth,
				})
			}
		}
	}

	return requests
}
