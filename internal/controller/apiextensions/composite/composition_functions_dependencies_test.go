/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package composite

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composite"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xcrd"

	v1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	fnv1 "github.com/crossplane/crossplane/v2/proto/fn/v1"
)

// TestDependencyOrderingAndRecovery exercises the full Compose path with three
// resources whose dependencies form cluster -> database -> app. It simulates a
// failed database create, then recovers it, and checks that only resources
// whose dependencies are ready are created.
func TestDependencyOrderingAndRecovery(t *testing.T) {
	ctx := context.Background()

	desired := chainFunction(t)
	observed := ComposedResourceStates{}
	created := map[string]int{}
	seen := map[string]bool{}
	failCreate := map[string]bool{"database": true}

	c := &test.MockClient{
		MockGet:         mockGetFunction(test.NewMockGetFn(nil)),
		MockList:        mockListFunctionRevisions(),
		MockStatusPatch: test.NewMockSubResourcePatchFn(nil),
		MockPatch: func(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
			cd, ok := obj.(*composed.Unstructured)
			if !ok {
				return nil
			}

			if failCreate[cd.GetName()] {
				return kerrors.NewInvalid(schema.GroupKind{Group: "example.org", Kind: "TestResource"}, cd.GetName(), nil)
			}
			if !seen[cd.GetName()] {
				seen[cd.GetName()] = true
				created[cd.GetName()]++
			}
			return nil
		},
	}

	composer := NewFunctionComposer(c, c, desired,
		WithComposedResourceObserver(ComposedResourceObserverFn(func(_ context.Context, _ resource.Composite) (ComposedResourceStates, error) {
			return observed, nil
		})),
		WithComposedResourceGarbageCollector(ComposedResourceGarbageCollectorFn(func(_ context.Context, _ metav1.Object, _, _ ComposedResourceStates) error {
			return nil
		})),
		WithCompositeConnectionDetailsFetcher(ConnectionDetailsFetcherFn(func(_ context.Context, _ ConnectionSecretOwner) (managed.ConnectionDetails, error) {
			return nil, nil
		})),
	)

	xr := composite.New(composite.WithGroupVersionKind(schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "XR"}))
	resetXR := func() {
		xr.SetName("parent")
		xr.SetUID("parent-uid")
		xr.SetGeneration(1)
		xr.SetLabels(map[string]string{xcrd.LabelKeyNamePrefixForComposed: "parent"})
	}
	resetXR()

	req := CompositionRequest{Revision: &v1.CompositionRevision{Spec: v1.CompositionRevisionSpec{
		Pipeline: []v1.PipelineStep{{
			Step:        "test",
			FunctionRef: &v1.FunctionReference{Name: "cool-function"},
		}},
	}}}

	// First pass: only cluster has no dependencies. database and app must wait.
	resetXR()
	res, err := composer.Compose(ctx, xr, req)
	if err != nil {
		t.Fatalf("first Compose(...): %v", err)
	}
	assertOnlyCreated(t, created, "cluster")
	assertResourceState(t, res, "database", xpv2.ReasonWaitingForDependencies)
	assertResourceState(t, res, "app", xpv2.ReasonWaitingForDependencies)

	ready, _ := AggregateCompositionConditions(res)
	if ready.Reason != xpv2.ReasonWaitingForDependencies {
		t.Fatalf("Ready reason = %q, want %q", ready.Reason, xpv2.ReasonWaitingForDependencies)
	}

	// Second pass: cluster is ready, so database is attempted but its create
	// fails. app still waits.
	resetXR()
	observed = ComposedResourceStates{"cluster": readyObserved("cluster")}
	res, err = composer.Compose(ctx, xr, req)
	if err != nil {
		t.Fatalf("second Compose(...): %v", err)
	}
	assertOnlyCreated(t, created, "cluster")
	assertResourceState(t, res, "database", xpv2.ReasonResourceCreationFailed)
	assertResourceState(t, res, "app", xpv2.ReasonWaitingForDependencies)

	// The database create fails, so the parent is not Ready and names it.
	ready, _ = AggregateCompositionConditions(res)
	if ready.Reason != xpv2.ReasonResourceCreationFailed {
		t.Fatalf("Ready reason = %q, want %q", ready.Reason, xpv2.ReasonResourceCreationFailed)
	}
	if len(ready.AffectedResources) != 1 || ready.AffectedResources[0] != "database" {
		t.Fatalf("Ready affected resources = %v, want [database]", ready.AffectedResources)
	}

	// Third pass: the database configuration is fixed. cluster is still ready,
	// so database is created. It has not been created twice.
	delete(failCreate, "database")
	resetXR()
	res, err = composer.Compose(ctx, xr, req)
	if err != nil {
		t.Fatalf("third Compose(...): %v", err)
	}
	assertOnlyCreated(t, created, "cluster", "database")
	assertResourceState(t, res, "app", xpv2.ReasonWaitingForDependencies)

	// Fourth pass: database is now ready, so app is finally created. Existing
	// ready resources are not created again.
	observed = ComposedResourceStates{
		"cluster":  readyObserved("cluster"),
		"database": readyObserved("database"),
	}
	resetXR()
	res, err = composer.Compose(ctx, xr, req)
	if err != nil {
		t.Fatalf("fourth Compose(...): %v", err)
	}
	assertOnlyCreated(t, created, "cluster", "database", "app")

	ready, _ = AggregateCompositionConditions(res)
	if ready.Reason != xpv2.ReasonCreating || ready.Status != corev1.ConditionFalse {
		t.Fatalf("Ready = %s/%s, want False/Creating", ready.Status, ready.Reason)
	}

	// Fifth pass: every resource is ready. The parent reports SyncComplete and
	// nothing is created again.
	observed = ComposedResourceStates{
		"cluster":  readyObserved("cluster"),
		"database": readyObserved("database"),
		"app":      readyObserved("app"),
	}
	resetXR()
	res, err = composer.Compose(ctx, xr, req)
	if err != nil {
		t.Fatalf("fifth Compose(...): %v", err)
	}
	assertOnlyCreated(t, created, "cluster", "database", "app")

	ready, _ = AggregateCompositionConditions(res)
	if ready.Reason != xpv2.ReasonSyncComplete || ready.Status != corev1.ConditionTrue {
		t.Fatalf("Ready = %s/%s, want True/SyncComplete", ready.Status, ready.Reason)
	}
}

func chainFunction(t *testing.T) FunctionRunner {
	t.Helper()

	resource := func(name, dependsOn string) *fnv1.Resource {
		md := map[string]any{"name": name}
		if dependsOn != "" {
			md["annotations"] = map[string]any{AnnotationKeyDependsOn: dependsOn}
		}
		return &fnv1.Resource{Resource: MustStruct(map[string]any{
			"apiVersion": "example.org/v1",
			"kind":       "TestResource",
			"metadata":   md,
			"spec":       map[string]any{"name": name},
		})}
	}

	return FunctionRunnerFn(func(_ context.Context, _ string, req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {
		resources := map[string]*fnv1.Resource{
			"cluster":  resource("cluster", ""),
			"database": resource("database", "cluster"),
			"app":      resource("app", "database"),
		}
		for name := range resources {
			if _, ok := req.GetObserved().GetResources()[name]; ok {
				resources[name].Ready = fnv1.Ready_READY_TRUE
			}
		}

		return &fnv1.RunFunctionResponse{
			Desired: &fnv1.State{Resources: resources},
		}, nil
	})
}

func assertOnlyCreated(t *testing.T, created map[string]int, want ...string) {
	t.Helper()

	got := make(map[string]bool, len(created))
	for name, count := range created {
		if count != 1 {
			t.Errorf("resource %q was created %d times, want 1", name, count)
		}
		got[name] = true
	}

	wantSet := make(map[string]bool, len(want))
	for _, name := range want {
		wantSet[name] = true
	}

	for name := range wantSet {
		if !got[name] {
			t.Errorf("resource %q was not created", name)
		}
	}
	for name := range got {
		if !wantSet[name] {
			t.Errorf("resource %q was created early", name)
		}
	}
}

func assertResourceState(t *testing.T, res CompositionResult, name string, reason xpv2.ConditionReason) {
	t.Helper()

	for _, cd := range res.Composed {
		if cd.ResourceName != ResourceName(name) {
			continue
		}
		if cd.FailureReason != reason {
			t.Errorf("resource %q reason = %q, want %q", name, cd.FailureReason, reason)
		}
		return
	}
	t.Errorf("resource %q not found in composition result", name)
}
