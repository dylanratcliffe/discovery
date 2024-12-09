package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/overmindtech/sdp-go"
)

// AdapterHost This struct holds references to all Adapters in a process
// and provides utility functions to work with them. Methods of this
// struct are safe to call concurrently.
type AdapterHost struct {
	// Map of types to all adapters for that type
	adapters []Adapter
	mutex    sync.RWMutex
}

func NewAdapterHost() *AdapterHost {
	sh := &AdapterHost{
		adapters: make([]Adapter, 0),
	}

	// Add meta-adapters so that we can respond to queries for `overmind-type`,
	// `overmind-scope` and `overmind-adapter` resources
	sh.addBuiltinAdapters()

	return sh
}

func (sh *AdapterHost) addBuiltinAdapters() {
	_ = sh.AddAdapters(&TypeAdapter{sh: sh})
	_ = sh.AddAdapters(&ScopeAdapter{sh: sh})
	_ = sh.AddAdapters(&SourcesAdapter{sh: sh})
}

var ErrAdapterAlreadyExists = errors.New("adapter already exists")

// AddAdapters Adds an adapter to this engine
func (sh *AdapterHost) AddAdapters(adapters ...Adapter) error {
	sh.mutex.Lock()
	defer sh.mutex.Unlock()

	for _, adapter := range adapters {
		// Validate that we don't already have an adapter for this type that has
		// overlapping scopes. I realise that this isn't very efficient, but
		// the number of adapters is expected to be low, so it should be fine
		for _, existingAdapter := range sh.adapters {
			if existingAdapter.Type() == adapter.Type() {
				for _, scope := range adapter.Scopes() {
					for _, existingScope := range existingAdapter.Scopes() {
						if existingScope == scope {
							return fmt.Errorf("adapter %s already exists with scope %s", adapter.Type(), scope)
						}
					}
				}
			}
		}

		sh.adapters = append(sh.adapters, adapter)
	}

	return nil
}

// Adapters Returns a slice of all known adapters
func (sh *AdapterHost) Adapters() []Adapter {
	sh.mutex.RLock()
	defer sh.mutex.RUnlock()

	adapters := make([]Adapter, 0)

	for _, adapter := range sh.adapters {
		adapters = append(adapters, adapter)
	}

	return adapters
}

// VisibleAdapters Returns a slice of all known adapters excluding hidden ones
func (sh *AdapterHost) VisibleAdapters() []Adapter {
	allAdapters := sh.Adapters()
	result := make([]Adapter, 0)

	// Add all adapters unless they are hidden
	for _, adapter := range allAdapters {
		if hs, ok := adapter.(HiddenAdapter); ok {
			if hs.Hidden() {
				// If the adapter is hidden, continue without adding it
				continue
			}
		}

		result = append(result, adapter)
	}

	return result
}

// AdapterByType Returns the adapters for a given type
func (sh *AdapterHost) AdaptersByType(typ string) []Adapter {
	sh.mutex.RLock()
	defer sh.mutex.RUnlock()

	adapters := make([]Adapter, 0)

	for _, adapter := range sh.adapters {
		if adapter.Type() == typ {
			adapters = append(adapters, adapter)
		}
	}

	return adapters
}

// ExpandQuery Expands queries with wildcards to no longer contain wildcards.
// Meaning that if we support 5 types, and a query comes in with a wildcard
// type, this function will expand that query into 5 queries, one for each
// type.
//
// The same goes for scopes, if we have a query with a wildcard scope, and
// a single adapter that supports 5 scopes, we will end up with 5 queries. The
// exception to this is if we have a adapter that supports all scopes, but is
// unable to list them. In this case there will still be some queries with
// wildcard scopes as they can't be expanded
//
// This functions returns a map of queries with the adapters that they should be
// run against
func (sh *AdapterHost) ExpandQuery(q *sdp.Query) map[*sdp.Query]Adapter {
	var checkAdapters []Adapter

	if IsWildcard(q.GetType()) {
		// If the query has a wildcard type, all non-hidden adapters might try
		// to respond
		checkAdapters = sh.VisibleAdapters()
	} else {
		// If the type is specific, pull just adapters for that type
		checkAdapters = append(checkAdapters, sh.AdaptersByType(q.GetType())...)
	}

	expandedQueries := make(map[*sdp.Query]Adapter)

	for _, adapter := range checkAdapters {
		// is the adapter is hidden
		isHidden := false
		if hs, ok := adapter.(HiddenAdapter); ok {
			isHidden = hs.Hidden()
		}

		for _, adapterScope := range adapter.Scopes() {
			// Create a new query if:
			//
			// * The adapter supports all scopes, or
			// * The query scope is a wildcard (and the adapter is not hidden), or
			// * The query scope substring matches adapter scope
			if IsWildcard(adapterScope) || (IsWildcard(q.GetScope()) && !isHidden) || strings.Contains(adapterScope, q.GetScope()) {
				dest := sdp.Query{}
				q.Copy(&dest)

				dest.Type = adapter.Type()

				// Choose the more specific scope
				if IsWildcard(adapterScope) {
					dest.Scope = q.GetScope()
				} else {
					dest.Scope = adapterScope
				}

				expandedQueries[&dest] = adapter
			}
		}
	}

	return expandedQueries
}

// ClearAllAdapters Removes all adapters from the engine
func (sh *AdapterHost) ClearAllAdapters() {
	sh.mutex.Lock()
	sh.adapters = make([]Adapter, 0)
	sh.mutex.Unlock()

	sh.addBuiltinAdapters()
}

// StartPurger Starts the purger for all caching adapters
func (sh *AdapterHost) StartPurger(ctx context.Context) {
	for _, s := range sh.Adapters() {
		if c, ok := s.(CachingAdapter); ok {
			cache := c.Cache()
			if cache != nil {
				err := cache.StartPurger(ctx)
				if err != nil {
					sentry.CaptureException(fmt.Errorf("failed to start purger for adapter %s: %w", s.Name(), err))
				}
			}
		}
	}
}

func (sh *AdapterHost) Purge() {
	for _, s := range sh.Adapters() {
		if c, ok := s.(CachingAdapter); ok {
			cache := c.Cache()
			if cache != nil {
				cache.Purge(time.Now())
			}
		}
	}
}

// ClearCaches Clears caches for all caching adapters
func (sh *AdapterHost) ClearCaches() {
	for _, s := range sh.Adapters() {
		if c, ok := s.(CachingAdapter); ok {
			cache := c.Cache()
			if cache != nil {
				cache.Clear()
			}
		}
	}
}
