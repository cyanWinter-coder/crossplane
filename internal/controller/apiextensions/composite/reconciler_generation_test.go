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

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composite"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func TestIsStaleCompositionResult(t *testing.T) {
	cases := map[string]struct {
		res        CompositionResult
		generation int64
		want       bool
	}{
		"Current": {
			res:        CompositionResult{ObservedGeneration: 2},
			generation: 2,
		},
		"Newer": {
			res:        CompositionResult{ObservedGeneration: 3},
			generation: 2,
		},
		"Stale": {
			res:        CompositionResult{ObservedGeneration: 1},
			generation: 2,
			want:       true,
		},
		"UnknownGeneration": {
			res:        CompositionResult{},
			generation: 2,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isStaleCompositionResult(tc.res, tc.generation); got != tc.want {
				t.Errorf("isStaleCompositionResult(...) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHandleCommonCompositionResultGeneration(t *testing.T) {
	xr := composite.New()
	xr.SetGeneration(7)
	xr.SetConditions(xpv2.Condition{
		Type:               "DatabaseReady",
		Status:             corev1.ConditionTrue,
		Reason:             "DatabaseOnline",
		ObservedGeneration: 7,
	})

	r := NewReconciler(&test.MockClient{}, schema.GroupVersionKind{})
	r.handleCommonCompositionResult(context.Background(), CompositionResult{
		Conditions: []TargetedCondition{
			{
				Condition: xpv2.Condition{
					Type:               "DatabaseReady",
					Status:             corev1.ConditionFalse,
					Reason:             "DatabaseOffline",
					ObservedGeneration: 6,
				},
				Target: CompositionTargetComposite,
			},
			{
				Condition: xpv2.Condition{
					Type:   "BucketReady",
					Status: corev1.ConditionTrue,
					Reason: "BucketOnline",
				},
				Target: CompositionTargetComposite,
			},
		},
	}, xr)

	// A result for generation 6 must not replace a condition that already
	// observed generation 7.
	database := xr.GetCondition("DatabaseReady")
	if database.Status != corev1.ConditionTrue || database.Reason != "DatabaseOnline" || database.ObservedGeneration != 7 {
		t.Errorf("stale condition replaced newer condition: %+v", database)
	}

	// Conditions without an explicit generation are stamped with the current
	// generation.
	bucket := xr.GetCondition("BucketReady")
	if bucket.ObservedGeneration != 7 {
		t.Errorf("BucketReady observed generation = %d, want 7", bucket.ObservedGeneration)
	}
}

func TestAggregateConditionsObservedGeneration(t *testing.T) {
	xr := composite.New()
	xr.SetGeneration(12)

	r := NewReconciler(&test.MockClient{}, schema.GroupVersionKind{})
	r.conditions.For(xr).MarkConditions(xpv2.SyncComplete("database"))

	ready := xr.GetCondition(xpv2.TypeReady)
	if ready.ObservedGeneration != 12 {
		t.Errorf("Ready observed generation = %d, want 12", ready.ObservedGeneration)
	}

	want := []string{"database"}
	if diff := cmp.Diff(want, ready.AffectedResources); diff != "" {
		t.Errorf("Ready affected resources: -want, +got:\n%s", diff)
	}
}
