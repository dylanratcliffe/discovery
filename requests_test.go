package discovery

import (
	"testing"

	"github.com/dylanratcliffe/sdp-go"
)

func TestExecuteRequest(t *testing.T) {
	e := Engine{
		Name: "test",
	}

	src := TestSource{
		ReturnType:     "person",
		ReturnContexts: []string{"test"},
	}

	e.AddSources(&src)

	t.Run("Basic happy-path Get request", func(t *testing.T) {
		request := &sdp.ItemRequest{
			Type:            "person",
			Method:          sdp.RequestMethod_GET,
			Query:           "foo",
			Context:         "test",
			LinkDepth:       3,
			ItemSubject:     "items",
			ResponseSubject: "responses",
		}

		items, err := e.ExecuteRequest(request)

		if err != nil {
			t.Error(err)
		}

		if x := len(src.GetCalls); x != 1 {
			t.Errorf("expected source's Get() to have been called 1 time, got %v", x)
		}

		if x := len(items); x != 1 {
			t.Errorf("expected 1 item, got %v", x)
		}

		item := items[0]
		itemRequest := item.LinkedItemRequests[0]

		if ld := itemRequest.LinkDepth; ld != 2 {
			t.Errorf("expected linked item depth to be 1 less than the query (2), got %v", ld)
		}

		if is := itemRequest.ItemSubject; is != "items" {
			t.Errorf("expected linked item request itemsubject to be \"items\" got %v", is)
		}

		if is := itemRequest.ResponseSubject; is != "responses" {
			t.Errorf("expected linked item request ResponseSubject to be \"responses\" got %v", is)
		}

		if item.Metadata.SourceRequest != request {
			t.Error("source request mismatch")
		}

	})

	t.Run("Wrong context Get request", func(t *testing.T) {
		request := &sdp.ItemRequest{
			Type:      "person",
			Method:    sdp.RequestMethod_GET,
			Query:     "foo",
			Context:   "wrong",
			LinkDepth: 0,
		}

		_, err := e.ExecuteRequest(request)

		if ire, ok := err.(*sdp.ItemRequestError); ok {
			if ire.ErrorType != sdp.ItemRequestError_NOCONTEXT {
				t.Errorf("expected error type to be NOCONTEXT, got %v", ire.ErrorType)
			}
		} else {
			t.Errorf("expected error to be ItemRequestError, got %T", err)
		}
	})

	t.Run("Wrong type Get request", func(t *testing.T) {
		request := &sdp.ItemRequest{
			Type:      "house",
			Method:    sdp.RequestMethod_GET,
			Query:     "foo",
			Context:   "test",
			LinkDepth: 0,
		}

		_, err := e.ExecuteRequest(request)

		if ire, ok := err.(*sdp.ItemRequestError); ok {
			if ire.ErrorType != sdp.ItemRequestError_NOCONTEXT {
				t.Errorf("expected error type to be NOCONTEXT, got %v", ire.ErrorType)
			}
		} else {
			t.Errorf("expected error to be ItemRequestError, got %T", err)
		}
	})

	t.Run("Basic Find request", func(t *testing.T) {
		request := &sdp.ItemRequest{
			Type:      "person",
			Method:    sdp.RequestMethod_FIND,
			Context:   "test",
			LinkDepth: 5,
		}

		items, err := e.ExecuteRequest(request)

		if err != nil {
			t.Error(err)
		}

		if len(items) < 1 {
			t.Error("expected at least one item")
		}
	})

	t.Run("Basic Search request", func(t *testing.T) {
		request := &sdp.ItemRequest{
			Type:      "person",
			Method:    sdp.RequestMethod_SEARCH,
			Query:     "TEST",
			Context:   "test",
			LinkDepth: 5,
		}

		items, err := e.ExecuteRequest(request)

		if err != nil {
			t.Error(err)
		}

		if len(items) < 1 {
			t.Error("expected at least one item")
		}
	})

}

// func TestNewItemRequestHandler(t *testing.T) {
// 	e := Engine{
// 		Name: "test",
// 	}

// 	src := TestSource{
// 		ReturnType:     "person",
// 		ReturnContexts: []string{
// 			"test1",
// 			"test2",
// 		},
// 	}

// 	e.AddSources(
// 		&TestSource{
// 			ReturnType:     "person",
// 			ReturnContexts: []string{
// 				"test1",
// 				"test2",
// 			},
// 		},
// 		&TestSource{
// 			ReturnType: "dog",
// 			ReturnContexts: []string{
// 				"testA",
// 				"testB",
// 			},
// 		}
// 	)

// }
