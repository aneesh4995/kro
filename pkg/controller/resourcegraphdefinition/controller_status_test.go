// Copyright 2025 The Kube Resource Orchestrator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package resourcegraphdefinition

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kro-run/kro/api/v1alpha1"
)

func TestSetResourceGraphDefinitionStatus_CELMetrics(t *testing.T) {
	testScheme := runtime.NewScheme()
	v1alpha1.AddToScheme(testScheme) // Add RGD types to scheme

	toInt64Ptr := func(val int64) *int64 { return &val }

	tests := []struct {
		name                 string
		initialRGD           *v1alpha1.ResourceGraphDefinition
		topologicalOrder     []string
		resourcesInfo        []v1alpha1.ResourceInformation
		inputCELMetrics      *v1alpha1.CELCostMetrics
		reconcileErr         error
		expectedStatusMetrics *v1alpha1.CELCostMetrics
	}{
		{
			name: "CELMetrics is nil",
			initialRGD: &v1alpha1.ResourceGraphDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rgd-nil", Namespace: "default"},
			},
			inputCELMetrics:      nil,
			expectedStatusMetrics: nil,
		},
		{
			name: "CELMetrics has data",
			initialRGD: &v1alpha1.ResourceGraphDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rgd-data", Namespace: "default"},
			},
			inputCELMetrics: &v1alpha1.CELCostMetrics{
				TotalCost: toInt64Ptr(100),
				CostPerResource: map[string]int64{
					"resource1": 60,
					"resource2": 40,
				},
			},
			expectedStatusMetrics: &v1alpha1.CELCostMetrics{
				TotalCost: toInt64Ptr(100),
				CostPerResource: map[string]int64{
					"resource1": 60,
					"resource2": 40,
				},
			},
		},
		{
			name: "CELMetrics has zero total cost and empty map",
			initialRGD: &v1alpha1.ResourceGraphDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rgd-zero", Namespace: "default"},
			},
			inputCELMetrics: &v1alpha1.CELCostMetrics{
				TotalCost:       toInt64Ptr(0),
				CostPerResource: make(map[string]int64),
			},
			expectedStatusMetrics: &v1alpha1.CELCostMetrics{
				TotalCost:       toInt64Ptr(0),
				CostPerResource: make(map[string]int64),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(tt.initialRGD).Build()
			
			reconciler := &ResourceGraphDefinitionReconciler{
				Client: fakeClient,
				// Other fields like instanceLogger can be nil or mocked if necessary for status updates
			}

			err := reconciler.setResourceGraphDefinitionStatus(
				context.Background(),
				tt.initialRGD,
				tt.topologicalOrder,
				tt.resourcesInfo,
				tt.inputCELMetrics,
				tt.reconcileErr,
			)

			if err != nil {
				t.Fatalf("setResourceGraphDefinitionStatus() error = %v", err)
			}

			updatedRGD := &v1alpha1.ResourceGraphDefinition{}
			err = fakeClient.Get(context.Background(), types.NamespacedName{Name: tt.initialRGD.Name, Namespace: tt.initialRGD.Namespace}, updatedRGD)
			if err != nil {
				t.Fatalf("Failed to get updated RGD: %v", err)
			}

			if !reflect.DeepEqual(updatedRGD.Status.CELMetrics, tt.expectedStatusMetrics) {
				t.Errorf("Status.CELMetrics got = %v, want %v", updatedRGD.Status.CELMetrics, tt.expectedStatusMetrics)
			}
		})
	}
}
