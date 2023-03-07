package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/overmindtech/sdp-go"
)

func TestTypeSource(t *testing.T) {
	s := &TypeSource{
		sh: newTestSourceHost(t),
	}

	t.Run("satisfies Source interface", func(t *testing.T) {
		//lint:ignore S1021 Testing that it satisfies the interface
		var src Source

		src = &TypeSource{}

		t.Log(src)
	})

	t.Run("listing types", func(t *testing.T) {
		items, err := s.List(context.Background(), "global")

		if err != nil {
			t.Error(err)
		}

		for _, item := range items {
			err = item.Validate()

			if err != nil {
				t.Error(err)
			}

			// Check that hidden types aren't included
			if item.UniqueAttributeValue() == "secret" {
				t.Error("hidden type included")
			}
		}

		if len(items) == 0 {
			t.Error("empty list")
		}
	})

	t.Run("get a specific type", func(t *testing.T) {
		item, err := s.Get(context.Background(), "global", "secret")

		if err != nil {
			t.Error(err)
		}

		err = item.Validate()

		if err != nil {
			t.Error(err)
		}
	})

	t.Run("get a bad type", func(t *testing.T) {
		_, err := s.Get(context.Background(), "global", "nothing-here")

		if err == nil {
			t.Error("expected error got nil")
		}

		var ire *sdp.QueryError

		if errors.As(err, &ire) {
			if ire.ErrorType != sdp.QueryError_NOTFOUND {
				t.Errorf("Expected error type NOTFOUND, got %v", ire.ErrorType)
			}
		}
	})
}

func TestScopeSource(t *testing.T) {
	s := &ScopeSource{
		sh: newTestSourceHost(t),
	}

	t.Run("satisfies Source interface", func(t *testing.T) {
		//lint:ignore S1021 Testing that it satisfies the interface
		var src Source

		src = &ScopeSource{}

		t.Log(src)
	})

	t.Run("listing Scopes", func(t *testing.T) {
		items, err := s.List(context.Background(), "global")

		if err != nil {
			t.Error(err)
		}

		for _, item := range items {
			err = item.Validate()

			if err != nil {
				t.Error(err)
			}

			// Check that hidden Scopes aren't included
			if item.UniqueAttributeValue() == "secret" {
				t.Error("hidden scope included")
			}
		}

		if len(items) == 0 {
			t.Error("empty list")
		}
	})

	t.Run("get a specific Scope", func(t *testing.T) {
		item, err := s.Get(context.Background(), "global", "secret")

		if err != nil {
			t.Error(err)
		}

		err = item.Validate()

		if err != nil {
			t.Error(err)
		}
	})

	t.Run("get a bad Scope", func(t *testing.T) {
		_, err := s.Get(context.Background(), "global", "nothing-here")

		if err == nil {
			t.Error("expected error got nil")
		}

		var ire *sdp.QueryError

		if errors.As(err, &ire) {
			if ire.ErrorType != sdp.QueryError_NOTFOUND {
				t.Errorf("Expected error Scope NOTFOUND, got %v", ire.ErrorType)
			}
		}
	})
}

func newTestSourceHost(t *testing.T) *SourceHost {
	sh, err := NewSourceHost()
	if err != nil {
		t.Fatalf("Error initializing SourceHost: %v", err)
	}
	sh.AddSources(
		&TestSource{
			ReturnType: "aws-ec2instance",
			ReturnScopes: []string{
				"prodAccountInternetBanking",
				"devAccountInternetBanking",
				"global",
			},
			ReturnName: "test-aws-ec2instance-source",
		},
		&TestSource{
			ReturnType: "aws-elasticloadbalancer",
			ReturnScopes: []string{
				"devAccountInternetBanking",
				"some-other-scope",
				"global",
			},
			ReturnName: "test-aws-elasticloadbalancer-source",
		},
		&TestSource{
			ReturnType: "ip",
			ReturnScopes: []string{
				"global",
			},
			ReturnName: "test-ip-source",
		},
		&TestSource{
			ReturnType: "secret",
			ReturnScopes: []string{
				"global",
				"secret",
			},
			ReturnName: "test-secret-source",
			IsHidden:   true,
		},
	)

	return sh
}
