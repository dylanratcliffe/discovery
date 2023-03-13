package discovery

import (
	"context"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/overmindtech/sdp-go"
)

type SlowSource struct {
	QueryDuration time.Duration
}

func (s *SlowSource) Type() string {
	return "person"
}

func (s *SlowSource) Name() string {
	return "slow-source"
}

func (s *SlowSource) DefaultCacheDuration() time.Duration {
	return 10 * time.Minute
}

func (s *SlowSource) Scopes() []string {
	return []string{"test"}
}

func (s *SlowSource) Hidden() bool {
	return false
}

func (s *SlowSource) Get(ctx context.Context, scope string, query string) (*sdp.Item, error) {
	end := time.Now().Add(s.QueryDuration)
	attributes, _ := sdp.ToAttributes(map[string]interface{}{
		"name": query,
	})

	item := sdp.Item{
		Type:              "person",
		UniqueAttribute:   "name",
		Attributes:        attributes,
		Scope:             "test",
		LinkedItemQueries: []*sdp.Query{},
	}

	for i := 0; i != 2; i++ {
		item.LinkedItemQueries = append(item.LinkedItemQueries, &sdp.Query{
			Type:   "person",
			Method: sdp.QueryMethod_GET,
			Query:  RandomName(),
			Scope:  "test",
		})
	}

	time.Sleep(time.Until(end))

	return &item, nil
}

func (s *SlowSource) List(ctx context.Context, scope string) ([]*sdp.Item, error) {
	return []*sdp.Item{}, nil
}

func (s *SlowSource) Weight() int {
	return 100
}

func TestParallelQueryPerformance(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "" {
		t.Skip("Performance tests under github actions are too unreliable")
	}

	// This test is designed to ensure that query duration is linear up to a
	// certain point. Above that point the overhead caused by having so many
	// goroutines running will start to make the response times non-linear which
	// maybe isn't ideal but given realistic loads we probably don't care.
	t.Run("Without linking", func(t *testing.T) {
		RunLinearPerformanceTest(t, "10 queries", 10, 0, 1)
		RunLinearPerformanceTest(t, "100 queries", 100, 0, 10)
		RunLinearPerformanceTest(t, "1,000 queries", 1000, 0, 100)
	})

	t.Run("With linking", func(t *testing.T) {
		RunLinearPerformanceTest(t, "1 query 3 depth", 1, 3, 1)
		RunLinearPerformanceTest(t, "1 query 3 depth", 1, 3, 100)
		RunLinearPerformanceTest(t, "1 query 5 depth", 1, 5, 100)
		RunLinearPerformanceTest(t, "10 queries 5 depth", 10, 5, 100)
	})
}

// RunLinearPerformanceTest Runs a test with a given number in input queries,
// link depth and parallelization limit. Expected results and expected duration
// are determined automatically meaning all this is testing for is the fact that
// the performance continues to be linear and predictable
func RunLinearPerformanceTest(t *testing.T, name string, numQueries int, linkDepth int, numParallel int) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		result := TimeQueries(numQueries, linkDepth, numParallel)

		if len(result.Results) != result.ExpectedItems {
			t.Errorf("Expected %v items, got %v (%v errors)", result.ExpectedItems, len(result.Results), len(result.Errors))
		}

		if result.TimeTaken > result.MaxTime {
			t.Errorf("Queries took too long: %v Max: %v", result.TimeTaken.String(), result.MaxTime.String())
		}
	})
}

type TimedResults struct {
	ExpectedItems int
	MaxTime       time.Duration
	TimeTaken     time.Duration
	Results       []*sdp.Item
	Errors        []*sdp.QueryError
}

func TimeQueries(numQueries int, linkDepth int, numParallel int) TimedResults {
	e, err := NewEngine()
	if err != nil {
		panic(fmt.Sprintf("Error initializing Engine: %v", err))
	}
	e.MaxParallelExecutions = numParallel
	e.AddSources(&SlowSource{
		QueryDuration: 100 * time.Millisecond,
	})
	e.Start()
	defer e.Stop()

	// Calculate how many items to expect and the expected duration
	var expectedItems int
	var expectedDuration time.Duration
	for i := 0; i <= linkDepth; i++ {
		thisLayer := int(math.Pow(2, float64(i))) * numQueries

		// Expect that it'll take no longer that 120% of the sleep time.
		thisDuration := 120 * math.Ceil(float64(thisLayer)/float64(numParallel))
		expectedDuration = expectedDuration + (time.Duration(thisDuration) * time.Millisecond)
		expectedItems = expectedItems + thisLayer
	}

	results := make([]*sdp.Item, 0)
	errors := make([]*sdp.QueryError, 0)
	resultsMutex := sync.Mutex{}
	wg := sync.WaitGroup{}

	start := time.Now()

	for i := 0; i < numQueries; i++ {
		qt := QueryTracker{
			Query: &sdp.Query{
				Type:      "person",
				Method:    sdp.QueryMethod_GET,
				Query:     RandomName(),
				Scope:     "test",
				LinkDepth: uint32(linkDepth),
			},
			Engine: e,
		}

		wg.Add(1)

		go func(qt *QueryTracker) {
			defer wg.Done()

			items, errs, _ := qt.Execute(context.Background())

			resultsMutex.Lock()
			results = append(results, items...)
			errors = append(errors, errs...)
			resultsMutex.Unlock()
		}(&qt)
	}

	wg.Wait()

	return TimedResults{
		ExpectedItems: expectedItems,
		MaxTime:       expectedDuration,
		TimeTaken:     time.Since(start),
		Results:       results,
		Errors:        errors,
	}
}
