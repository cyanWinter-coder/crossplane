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

// Package crdschema adjusts generated CRD schemas to include Crossplane
// condition fields that are newer than the schema helpers in crossplane-runtime.
package crdschema

import (
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"
)

// EnsureConditionAffectedResources adds the AffectedResources field to every
// status.conditions schema in the supplied CRD. The field is optional, so this
// is backwards compatible with conditions that don't set it.
func EnsureConditionAffectedResources(crd *extv1.CustomResourceDefinition) {
	if crd == nil {
		return
	}

	for i := range crd.Spec.Versions {
		schema := crd.Spec.Versions[i].Schema
		if schema == nil || schema.OpenAPIV3Schema == nil {
			continue
		}

		status, ok := schema.OpenAPIV3Schema.Properties["status"]
		if !ok {
			continue
		}
		conditions, ok := status.Properties["conditions"]
		if !ok || conditions.Items == nil || conditions.Items.Schema == nil {
			continue
		}

		props := conditions.Items.Schema.Properties
		if props == nil {
			props = make(map[string]extv1.JSONSchemaProps)
		}
		if _, exists := props["affectedResources"]; exists {
			continue
		}

		props["affectedResources"] = extv1.JSONSchemaProps{
			Description: "AffectedResources names the resources whose state caused this condition.",
			Type:        "array",
			Items: &extv1.JSONSchemaPropsOrArray{
				Schema: &extv1.JSONSchemaProps{Type: "string"},
			},
			XListType: ptr.To("set"),
		}
		conditions.Items.Schema.Properties = props
		status.Properties["conditions"] = conditions
		schema.OpenAPIV3Schema.Properties["status"] = status
	}
}
