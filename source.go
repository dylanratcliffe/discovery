package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/overmindtech/sdp-go"
	"github.com/overmindtech/sdpcache"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Source is capable of finding information about items
//
// Sources must implement all of the methods to satisfy this interface in order
// to be able to used as an SDP source. Note that the `context.Context` value
// that is passed to the Get(), Find() and Search() (optional) methods needs to
// handled by each source individually. Source authors should make an effort
// ensure that expensive operations that the source undertakes can be cancelled
// if the context `ctx` is cancelled
type Source interface {
	// Type The type of items that this source is capable of finding
	Type() string

	// Descriptive name for the source, used in logging and metadata
	Name() string

	// List of contexts that this source is capable of find items for. If the
	// source supports all contexts the special value "*"
	// should be used
	Contexts() []string

	// Get Get a single item with a given context and query. The item returned
	// should have a UniqueAttributeValue that matches the `query` parameter.
	//
	// Note that the itemContext parameter represents the context of the item
	// from the perspective of State Description Protocol (SDP), whereas the
	// `context.Context` value is a golang context which is used for
	// cancellations and timeouts
	Get(ctx context.Context, itemContext string, query string) (*sdp.Item, error)

	// Find Finds all items in a given context
	//
	// Note that the itemContext parameter represents the context of the item
	// from the perspective of State Description Protocol (SDP), whereas the
	// `context.Context` value is a golang context which is used for
	// cancellations and timeouts
	Find(ctx context.Context, itemContext string) ([]*sdp.Item, error)

	// Weight Returns the priority weighting of items returned by this source.
	// This is used to resolve conflicts where two sources of the same type
	// return an item for a GET request. In this instance only one item can be
	// sen on, so the one with the higher weight value will win.
	Weight() int
}

// SearchableItemSource Is a source of items that supports searching
type SearchableSource interface {
	Source
	// Search executes a specific search and returns zero or many items as a
	// result (and optionally an error). The specific format of the query that
	// needs to be provided to Search is dependant on the source itself as each
	// source will respond to searches differently
	//
	// Note that the itemContext parameter represents the context of the item
	// from the perspective of State Description Protocol (SDP), whereas the
	// `context.Context` value is a golang context which is used for
	// cancellations and timeouts
	Search(ctx context.Context, itemContext string, query string) ([]*sdp.Item, error)
}

// CacheDefiner Some backends may implement the CacheDefiner interface which
// means that they specify a custom default cache interval. The config will
// always take precedence however
type CacheDefiner interface {
	DefaultCacheDuration() time.Duration
}

// HiddenSource Sources that define a `Hidden()` method are able to tell whether
// or not the items they produce should be marked as hidden within the metadata.
// Hidden items will not be shown in GUIs or stored in databases and are used
// for gathering data as part of other proccesses such as remotely executed
// secondary sources
type HiddenSource interface {
	Hidden() bool
}

// GetCacheDuration Gets the cache duration for a specific source, or a default
// value
func GetCacheDuration(s Source) time.Duration {
	if cd, ok := s.(CacheDefiner); ok {
		return cd.DefaultCacheDuration()
	}

	return (10 * time.Minute)
}

// Get Runs a get query against known sources in priority order. If nothing was
// found, returns the first error
func (e *Engine) Get(ctx context.Context, r *sdp.ItemRequest, relevantSources []Source) (*sdp.Item, error) {
	if len(relevantSources) == 0 {
		return nil, &sdp.ItemRequestError{
			ErrorType:   sdp.ItemRequestError_NOCONTEXT,
			ErrorString: fmt.Sprintf("no sources found for type %v and context %v", r.Type, r.Context),
			Context:     r.Context,
		}
	}

	e.gfm.GetLock(r.Context, r.Type)

	var err error

	for _, src := range relevantSources {
		tags := sdpcache.Tags{
			"sourceName":           src.Name(),
			"uniqueAttributeValue": r.Query,
			"type":                 r.Type,
			"context":              r.Context,
		}

		logFields := log.Fields{
			"sourceName":   src.Name(),
			"sourceWeight": src.Weight(),
			"type":         r.Type,
			"context":      r.Context,
			"query":        r.Query,
		}

		if !r.IgnoreCache {
			cached, cacheErr := e.cache.Search(tags)

			switch err := cacheErr.(type) {
			case sdpcache.CacheNotFoundError:
				// If the item/error wasn't found in the cache then just continue on
			case *sdp.ItemRequestError:
				if err.ErrorType == sdp.ItemRequestError_NOTFOUND {
					// If the item wasn't found, but we've already looked then don't look
					// again and just return a blank result
					log.WithFields(logFields).Debug("Was not found previously, skipping")

					continue
				}
			case nil:
				if len(cached) == 1 {
					// If the cache found something then just return that
					log.WithFields(logFields).Debug("Found item from cache")

					e.gfm.GetUnlock(r.Context, r.Type)

					return cached[0], nil
				}

				// If we got a weird number of stuff from the cache then
				// something is wrong
				log.WithFields(logFields).Error("Cache returned >1 value, purging and continuing")

				e.cache.Delete(tags)
			}
		}

		log.WithFields(logFields).Debug("Executing get for backend")

		var getDuration time.Duration
		var item *sdp.Item
		err = nil

		getDuration = timeOperation(func() {
			item, err = src.Get(ctx, r.Context, r.Query)
		})

		logFields["itemFound"] = (err == nil)
		logFields["error"] = err

		// A good backend should be careful to raise ItemNotFoundError if the
		// query was able to execute successfully, but the item wasn't found. If
		// however there was some failure in checking and therefore we aren't
		// sure if the item is actually there is not another type of error
		// should be raised and this will be logged
		if ire, sdpErr := err.(*sdp.ItemRequestError); (sdpErr && ire.ErrorType == sdp.ItemRequestError_NOTFOUND) || err == nil {
			log.WithFields(logFields).Debug("Get complete")

			if ire != nil {
				// Cache the error since the types was ItemRequestError_NOTFOUND
				// and therefore the item doesn't exist
				e.cache.StoreError(err, GetCacheDuration(src), tags)
			}
		} else {
			log.WithFields(logFields).Error("Get Failed")
		}

		if err == nil {
			// Handle the case where we are given a nil pointer
			if item == nil {
				return &sdp.Item{}, &sdp.ItemRequestError{
					ErrorType:   sdp.ItemRequestError_OTHER,
					ErrorString: "Backend returned a nil pointer as an item",
				}
			}

			// Set the metadata
			item.Metadata = &sdp.Metadata{
				Timestamp:             timestamppb.New(time.Now()),
				SourceDuration:        durationpb.New(getDuration),
				SourceDurationPerItem: durationpb.New(getDuration),
				SourceName:            src.Name(),
			}

			// Mark the item as hidden if the source is a hidden source
			if hs, ok := src.(HiddenSource); ok {
				item.Metadata.Hidden = hs.Hidden()
			}

			// Store the new item in the cache
			e.cache.StoreItem(item, GetCacheDuration(src), tags)

			e.gfm.GetUnlock(r.Context, r.Type)

			return item, nil
		}

		e.gfm.GetUnlock(r.Context, r.Type)
	}

	// If we don't find anything then we should raise an error
	return &sdp.Item{}, err
}

// Find executes Find() on all sources for a given type, returning the merged
// results. Only returns an error if all sources fail, in which case returns the
// first error
func (e *Engine) Find(ctx context.Context, r *sdp.ItemRequest, relevantSources []Source) ([]*sdp.Item, error) {
	if len(relevantSources) == 0 {
		return nil, &sdp.ItemRequestError{
			ErrorType:   sdp.ItemRequestError_NOCONTEXT,
			ErrorString: fmt.Sprintf("no sources found for type %v and context %v", r.Type, r.Context),
			Context:     r.Context,
		}
	}

	e.gfm.FindLock(r.Context, r.Type)
	defer e.gfm.FindUnlock(r.Context, r.Type)

	items := make([]*sdp.Item, 0)
	errors := make([]error, 0)

	for _, src := range relevantSources {
		tags := sdpcache.Tags{
			"method":     "find",
			"sourceName": src.Name(),
			"context":    r.Context,
		}

		logFields := log.Fields{
			"sourceName": src.Name(),
			"type":       r.Type,
			"context":    r.Context,
		}

		if !r.IgnoreCache {
			cachedItems, err := e.cache.Search(tags)

			switch err := err.(type) {
			case sdpcache.CacheNotFoundError:
				// If the item/error wasn't found in the cache then just
				// continue on
			case *sdp.ItemRequestError:
				if err.ErrorType == sdp.ItemRequestError_NOTFOUND {
					log.WithFields(logFields).Debug("Found cached empty FIND, not executing")

					continue
				}
			default:
				// If we get a result from the cache then return that
				if len(cachedItems) > 0 {
					logFields["items"] = len(cachedItems)

					log.WithFields(logFields).Debug("Found items from cache")

					items = append(items, cachedItems...)
					errors = append(errors, err)

					continue
				}
			}
		}

		log.WithFields(logFields).Debug("Executing find")

		finds := make([]*sdp.Item, 0)
		var err error

		findDuration := timeOperation(func() {
			finds, err = src.Find(ctx, r.Context)
		})

		logFields["items"] = len(finds)
		logFields["error"] = err

		if err == nil {
			log.WithFields(logFields).Debug("Find complete")

			// Check too see if nothing was found, make sure we cache the
			// nothing
			if len(finds) == 0 {
				e.cache.StoreError(&sdp.ItemRequestError{
					ErrorType: sdp.ItemRequestError_NOTFOUND,
				}, GetCacheDuration(src), tags)
			}
		} else {
			log.WithFields(logFields).Error("Error during find")

			e.cache.StoreError(err, GetCacheDuration(src), tags)
		}

		// For each found item, add more details
		//
		// Use the index here to ensure that we're actually editing the
		// right thing
		for i := range finds {
			// Get a pointer to the item we're dealing with
			item := finds[i]

			// Handle the case where we are given a nil pointer
			if item == nil {
				continue
			}

			// Store metadata
			item.Metadata = &sdp.Metadata{
				Timestamp:             timestamppb.New(time.Now()),
				SourceDuration:        durationpb.New(findDuration),
				SourceDurationPerItem: durationpb.New(time.Duration(findDuration.Nanoseconds() / int64(len(finds)))),
				SourceName:            src.Name(),
			}

			// Mark the item as hidden if the source is a hidden source
			if hs, ok := src.(HiddenSource); ok {
				item.Metadata.Hidden = hs.Hidden()
			}

			// Cache the item
			e.cache.StoreItem(item, GetCacheDuration(src), tags)
		}

		items = append(items, finds...)
		errors = append(errors, err)
	}

	// Check if there were any successful runs and if so return the items
	for _, e := range errors {
		if e == nil {
			return items, nil
		}
	}

	if len(errors) > 0 {
		return items, errors[0]
	}

	return items, nil
}

// Search executes Search() on all sources for a given type, returning the merged
// results. Only returns an error if all sources fail, in which case returns the
// first error
func (e *Engine) Search(ctx context.Context, r *sdp.ItemRequest, relevantSources []Source) ([]*sdp.Item, error) {
	searchableSources := make([]SearchableSource, 0)

	// Filter further by searchability
	for _, source := range relevantSources {
		if searchable, ok := source.(SearchableSource); ok {
			searchableSources = append(searchableSources, searchable)
		}
	}

	if len(searchableSources) == 0 {
		return nil, &sdp.ItemRequestError{
			ErrorType:   sdp.ItemRequestError_NOCONTEXT,
			ErrorString: fmt.Sprintf("no sources found for type %v and context %v that support searching", r.Type, r.Context),
			Context:     r.Context,
		}
	}

	e.gfm.GetLock(r.Context, r.Type)
	defer e.gfm.GetUnlock(r.Context, r.Type)

	items := make([]*sdp.Item, 0)
	errors := make([]error, 0)

	for _, src := range searchableSources {
		tags := sdpcache.Tags{
			"method":     "find",
			"sourceName": src.Name(),
			"query":      r.Query,
			"context":    r.Context,
		}

		logFields := log.Fields{
			"sourceName": src.Name(),
			"type":       r.Type,
			"context":    r.Context,
		}

		if !r.IgnoreCache {
			cachedItems, err := e.cache.Search(tags)

			switch err := err.(type) {
			case sdpcache.CacheNotFoundError:
				// If the item/error wasn't found in the cache then just
				// continue on
			case *sdp.ItemRequestError:
				if err.ErrorType == sdp.ItemRequestError_NOTFOUND {
					log.WithFields(logFields).Debug("Found cached empty result, not executing")

					continue
				}
			default:
				// If we get a result from the cache then return that
				if len(cachedItems) > 0 {
					logFields["items"] = len(cachedItems)

					log.WithFields(logFields).Debug("Found items from cache")

					items = append(items, cachedItems...)
					errors = append(errors, err)

					continue
				}
			}
		}

		log.WithFields(logFields).Debug("Executing search")

		var searchItems []*sdp.Item
		var err error

		searchDuration := timeOperation(func() {
			searchItems, err = src.Search(ctx, r.Context, r.Query)
		})

		logFields["items"] = len(searchItems)
		logFields["error"] = err

		if err == nil {
			log.WithFields(logFields).Debug("Search completed")

			// Check too see if nothing was found, make sure we cache the
			// nothing
			if len(searchItems) == 0 {
				e.cache.StoreError(&sdp.ItemRequestError{
					ErrorType: sdp.ItemRequestError_NOTFOUND,
				}, GetCacheDuration(src), tags)
			}
		} else {
			log.WithFields(logFields).Error("Error during search")

			e.cache.StoreError(err, GetCacheDuration(src), tags)
		}

		// For each found item, add more details
		//
		// Use the index here to ensure that we're actually editing the
		// right thing
		for i := range searchItems {
			// Get a pointer to the item we're dealing with
			item := searchItems[i]

			// Handle the case where we are given a nil pointer
			if item == nil {
				continue
			}

			// Store metadata
			item.Metadata = &sdp.Metadata{
				Timestamp:             timestamppb.New(time.Now()),
				SourceDuration:        durationpb.New(searchDuration),
				SourceDurationPerItem: durationpb.New(time.Duration(searchDuration.Nanoseconds() / int64(len(searchItems)))),
				SourceName:            src.Name(),
			}

			// Mark the item as hidden if the source is a hidden source
			if hs, ok := src.(HiddenSource); ok {
				item.Metadata.Hidden = hs.Hidden()
			}

			// Cache the item
			e.cache.StoreItem(item, GetCacheDuration(src), tags)
		}

		items = append(items, searchItems...)
		errors = append(errors, err)
	}

	// Check if there were any successful runs and if so return the items
	for _, e := range errors {
		if e == nil {
			return items, nil
		}
	}

	if len(errors) > 0 {
		return items, errors[0]
	}

	return items, nil
}

// timeOperation Times how lon an operation takes and stores it in the first
// parameter. The second parameter is the function to execute
func timeOperation(f func()) time.Duration {
	start := time.Now()

	f()

	return time.Since(start)
}
