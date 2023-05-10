package discovery

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/overmindtech/sdp-go"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

type SpeedTestSource struct {
	QueryDelay   time.Duration
	ReturnType   string
	ReturnScopes []string
}

func (s *SpeedTestSource) Type() string {
	if s.ReturnType != "" {
		return s.ReturnType
	}

	return "person"
}

func (s *SpeedTestSource) Name() string {
	return "SpeedTestSource"
}

func (s *SpeedTestSource) Scopes() []string {
	if len(s.ReturnScopes) > 0 {
		return s.ReturnScopes
	}

	return []string{"test"}
}

func (s *SpeedTestSource) Get(ctx context.Context, scope string, query string) (*sdp.Item, error) {
	select {
	case <-time.After(s.QueryDelay):
		return &sdp.Item{
			Type:            s.Type(),
			UniqueAttribute: "name",
			Attributes: &sdp.ItemAttributes{
				AttrStruct: &structpb.Struct{
					Fields: map[string]*structpb.Value{
						"name": {
							Kind: &structpb.Value_StringValue{
								StringValue: query,
							},
						},
					},
				},
			},
			LinkedItemQueries: []*sdp.LinkedItemQuery{
				{
					Query: &sdp.Query{
						Type:   "person",
						Method: sdp.QueryMethod_GET,
						Query:  query + time.Now().String(),
						Scope:  scope,
					},
				},
			},
			Scope: scope,
		}, nil
	case <-ctx.Done():
		return nil, &sdp.QueryError{
			ErrorType:   sdp.QueryError_TIMEOUT,
			ErrorString: ctx.Err().Error(),
			Scope:       scope,
		}
	}

}

func (s *SpeedTestSource) List(ctx context.Context, scope string) ([]*sdp.Item, error) {
	item, err := s.Get(ctx, scope, "dylan")

	return []*sdp.Item{item}, err
}

func (s *SpeedTestSource) Weight() int {
	return 10
}

func TestExecute(t *testing.T) {
	src := TestSource{
		ReturnType: "person",
		ReturnScopes: []string{
			"test",
		},
	}

	e := newStartedEngine(t, "TestExecute", nil, &src)

	t.Run("Without linking", func(t *testing.T) {
		t.Parallel()

		qt := QueryTracker{
			Engine: e,
			Query: &sdp.Query{
				Type:   "person",
				Method: sdp.QueryMethod_GET,
				Query:  "Dylan",
				RecursionBehaviour: &sdp.Query_RecursionBehaviour{
					LinkDepth: 0,
				},
				Scope: "test",
			},
		}

		items, errs, err := qt.Execute(context.Background())

		if err != nil {
			t.Error(err)
		}

		for _, e := range errs {
			t.Error(e)
		}

		if l := len(items); l != 1 {
			t.Errorf("expected 1 items, got %v", l)
		}
	})

	t.Run("With linking", func(t *testing.T) {
		t.Parallel()

		qt := QueryTracker{
			Engine: e,
			Query: &sdp.Query{
				Type:   "person",
				Method: sdp.QueryMethod_GET,
				Query:  "Dylan",
				RecursionBehaviour: &sdp.Query_RecursionBehaviour{
					LinkDepth: 10,
				},
				Scope: "test",
			},
		}

		items, errs, err := qt.Execute(context.Background())

		if err != nil {
			t.Error(err)
		}

		for _, e := range errs {
			t.Error(e)
		}

		if l := len(items); l != 11 {
			t.Errorf("expected 10 items, got %v", l)
		}
	})

	t.Run("With no engine", func(t *testing.T) {
		t.Parallel()

		qt := QueryTracker{
			Engine: nil,
			Query: &sdp.Query{
				Type:   "person",
				Method: sdp.QueryMethod_GET,
				Query:  "Dylan",
				RecursionBehaviour: &sdp.Query_RecursionBehaviour{
					LinkDepth: 10,
				},
				Scope: "test",
			},
		}

		_, _, err := qt.Execute(context.Background())

		if err == nil {
			t.Error("expected error but got nil")
		}
	})

	t.Run("With no queries", func(t *testing.T) {
		t.Parallel()

		qt := QueryTracker{
			Engine: e,
		}

		_, _, err := qt.Execute(context.Background())

		if err != nil {
			t.Error(err)
		}
	})

}

func TestTimeout(t *testing.T) {
	src := SpeedTestSource{
		QueryDelay: 100 * time.Millisecond,
	}
	e := newStartedEngine(t, "TestTimeout", nil, &src)

	t.Run("With a timeout, but not exceeding it", func(t *testing.T) {
		t.Parallel()

		qt := QueryTracker{
			Engine: e,
			Query: &sdp.Query{
				Type:   "person",
				Method: sdp.QueryMethod_GET,
				Query:  "Dylan",
				RecursionBehaviour: &sdp.Query_RecursionBehaviour{
					LinkDepth: 0,
				},
				Scope:   "test",
				Timeout: durationpb.New(200 * time.Millisecond),
			},
		}

		items, errs, err := qt.Execute(context.Background())

		if err != nil {
			t.Error(err)
		}

		for _, e := range errs {
			t.Error(e)
		}

		if l := len(items); l != 1 {
			t.Errorf("expected 1 items, got %v", l)
		}
	})

	t.Run("With a timeout that is exceeded", func(t *testing.T) {
		t.Parallel()

		qt := QueryTracker{
			Engine: e,
			Query: &sdp.Query{
				Type:   "person",
				Method: sdp.QueryMethod_GET,
				Query:  "somethingElse",
				RecursionBehaviour: &sdp.Query_RecursionBehaviour{
					LinkDepth: 0,
				},
				Scope:   "test",
				Timeout: durationpb.New(50 * time.Millisecond),
			},
		}

		_, _, err := qt.Execute(context.Background())

		if err == nil {
			t.Error("Expected timout but got no error")
		}
	})

	t.Run("With linking that exceeds the timout", func(t *testing.T) {
		qt := QueryTracker{
			Engine: e,
			Query: &sdp.Query{
				Type:   "person",
				Method: sdp.QueryMethod_GET,
				Query:  "somethingElse1",
				RecursionBehaviour: &sdp.Query_RecursionBehaviour{
					LinkDepth: 10,
				},
				Scope:   "test",
				Timeout: durationpb.New(350 * time.Millisecond),
			},
		}

		items, errs, err := qt.Execute(context.Background())

		if err == nil {
			t.Error("Expected timeout but got no error")
		}

		for _, e := range errs {
			t.Error(e)
		}

		if len(items) != 3 {
			t.Errorf("Expected 3 items, got %v", len(items))
		}
	})
}

func TestCancel(t *testing.T) {
	e := newStartedEngine(t, "TestCancel", nil)

	u := uuid.New()

	qt := QueryTracker{
		Engine: e,
		Query: &sdp.Query{
			Type:   "person",
			Method: sdp.QueryMethod_GET,
			Query:  "somethingElse1",
			RecursionBehaviour: &sdp.Query_RecursionBehaviour{
				LinkDepth: 10,
			},
			Scope: "test",
			UUID:  u[:],
		},
	}

	items := make([]*sdp.Item, 0)
	var wg sync.WaitGroup

	var err error
	wg.Add(1)
	go func() {
		items, _, err = qt.Execute(context.Background())
		wg.Done()
	}()

	// Give it some time to populate the cancelFunc
	time.Sleep(100 * time.Millisecond)

	qt.Cancel()

	wg.Wait()

	if err == nil {
		t.Error("expected error but got none")
	}

	if len(items) != 0 {
		t.Errorf("Expected no items but got %v", items)
	}
}
