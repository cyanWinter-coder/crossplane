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

package crdschema

import (
	"testing"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestEnsureConditionAffectedResources(t *testing.T) {
	crd := &extv1.CustomResourceDefinition{Spec: extv1.CustomResourceDefinitionSpec{
		Versions: []extv1.CustomResourceDefinitionVersion{
			{
				Schema: &extv1.CustomResourceValidation{
					OpenAPIV3Schema: &extv1.JSONSchemaProps{
						Properties: map[string]extv1.JSONSchemaProps{
							"status": {
								Properties: map[string]extv1.JSONSchemaProps{
									"conditions": {
										Type: "array",
										Items: &extv1.JSONSchemaPropsOrArray{
											Schema: &extv1.JSONSchemaProps{
												Properties: map[string]extv1.JSONSchemaProps{
													"type": {Type: "string"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}}

	EnsureConditionAffectedResources(crd)

	conditions := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"].Properties["conditions"]
	props := conditions.Items.Schema.Properties

	if _, ok := props["type"]; !ok {
		t.Fatal("existing condition properties were removed")
	}
	affected, ok := props["affectedResources"]
	if !ok {
		t.Fatal("affectedResources was not added")
	}
	if affected.Type != "array" || affected.Items == nil || affected.Items.Schema == nil || affected.Items.Schema.Type != "string" {
		t.Errorf("affectedResources schema = %+v, want array of strings", affected)
	}
	if affected.XListType == nil || *affected.XListType != "set" {
		t.Errorf("affectedResources list type = %v, want set", affected.XListType)
	}
}
