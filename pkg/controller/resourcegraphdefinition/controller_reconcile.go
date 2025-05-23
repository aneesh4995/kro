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
	"fmt"
	"time"

	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kro-run/kro/api/v1alpha1"
	instancectrl "github.com/kro-run/kro/pkg/controller/instance"
	"github.com/kro-run/kro/pkg/dynamiccontroller"
	"github.com/kro-run/kro/pkg/graph"
	"github.com/kro-run/kro/pkg/metadata"
	kroruntime "github.com/kro-run/kro/pkg/runtime" // Added import for runtime
)

// reconcileResourceGraphDefinition orchestrates the reconciliation of a ResourceGraphDefinition by:
// 1. Processing the resource graph
// 2. Ensuring CRDs are present
// 3. Setting up and starting the microcontroller
func (r *ResourceGraphDefinitionReconciler) reconcileResourceGraphDefinition(ctx context.Context, rgd *v1alpha1.ResourceGraphDefinition) ([]string, []v1alpha1.ResourceInformation, *v1alpha1.CELCostMetrics, error) {
	log := ctrl.LoggerFrom(ctx)

	// Process resource graph definition graph first to validate structure
	log.V(1).Info("reconciling resource graph definition graph")
	processedRGD, resourcesInfo, err := r.reconcileResourceGraphDefinitionGraph(ctx, rgd)
	if err != nil {
		return nil, nil, nil, err
	}

	rt, err := kroruntime.NewResourceGraphDefinitionRuntime(processedRGD.Instance, processedRGD.Resources, processedRGD.TopologicalOrder, r.trackCELCosts) // Use r.trackCELCosts
	if err != nil {
		// Handle error - this could be a new error type or part of reconcileErr
		return processedRGD.TopologicalOrder, resourcesInfo, nil, fmt.Errorf("failed to create RGD runtime: %w", err)
	}
	// Loop to synchronize and resolve. This is typically done by the instance controller,
	// but for RGD status, we might need a simplified version if costs depend on resolved values.
	// For now, let's assume initial costs (static, includeWhen/readyWhen if they don't depend on live data) are available.
	// A full sync loop could be:
	// for {
	//     done, syncErr := rt.Synchronize()
	//     if syncErr != nil {
	//         return processedRGD.TopologicalOrder, resourcesInfo, nil, fmt.Errorf("runtime synchronization error: %w", syncErr)
	//     }
	//     if !done { // if done is true, it means continue, if false, it means break.
	//         break
	//     }
	// }

	// Aggregate CEL costs
	celCostMetrics, costErr := rt.AggregateCELCosts()
	if costErr != nil {
		// Log the error but don't necessarily fail the whole reconciliation
		log.Error(costErr, "failed to aggregate CEL costs")
	}

	// Setup metadata labeling
	graphExecLabeler, err := r.setupLabeler(rgd)
	if err != nil {
		return nil, nil, celCostMetrics, fmt.Errorf("failed to setup labeler: %w", err)
	}

	crd := processedRGD.Instance.GetCRD()
	graphExecLabeler.ApplyLabels(&crd.ObjectMeta)

	// Ensure CRD exists and is up to date
	log.V(1).Info("reconciling resource graph definition CRD")
	if err := r.reconcileResourceGraphDefinitionCRD(ctx, crd); err != nil {
		return processedRGD.TopologicalOrder, resourcesInfo, celCostMetrics, err
	}

	// Setup and start microcontroller
	gvr := processedRGD.Instance.GetGroupVersionResource()
	controller := r.setupMicroController(gvr, processedRGD, rgd.Spec.DefaultServiceAccounts, graphExecLabeler)

	log.V(1).Info("reconciling resource graph definition micro controller")
	// TODO: the context that is passed here is tied to the reconciliation of the rgd, we might need to make
	// a new context with our own cancel function here to allow us to cleanly term the dynamic controller
	// rather than have it ignore this context and use the background context.
	if err := r.reconcileResourceGraphDefinitionMicroController(ctx, &gvr, controller.Reconcile); err != nil {
		return processedRGD.TopologicalOrder, resourcesInfo, celCostMetrics, err
	}

	return processedRGD.TopologicalOrder, resourcesInfo, celCostMetrics, nil
}

// setupLabeler creates and merges the required labelers for the resource graph definition
func (r *ResourceGraphDefinitionReconciler) setupLabeler(rgd *v1alpha1.ResourceGraphDefinition) (metadata.Labeler, error) {
	rgLabeler := metadata.NewResourceGraphDefinitionLabeler(rgd)
	return r.metadataLabeler.Merge(rgLabeler)
}

// setupMicroController creates a new controller instance with the required configuration
func (r *ResourceGraphDefinitionReconciler) setupMicroController(
	gvr schema.GroupVersionResource,
	processedRGD *graph.Graph,
	defaultSVCs map[string]string,
	labeler metadata.Labeler,
) *instancectrl.Controller {
	instanceLogger := r.instanceLogger.WithName(fmt.Sprintf("%s-controller", gvr.Resource)).WithValues(
		"controller", gvr.Resource,
		"controllerGroup", processedRGD.Instance.GetCRD().Spec.Group,
		"controllerKind", processedRGD.Instance.GetCRD().Spec.Names.Kind,
	)

	return instancectrl.NewController(
		instanceLogger,
		instancectrl.ReconcileConfig{
			DefaultRequeueDuration:    3 * time.Second,
			DeletionGraceTimeDuration: 30 * time.Second,
			DeletionPolicy:            "Delete",
		},
		gvr,
		processedRGD,
		r.clientSet,
		defaultSVCs,
		labeler,
	)
}

// reconcileResourceGraphDefinitionGraph processes the resource graph definition to build a dependency graph
// and extract resource information
func (r *ResourceGraphDefinitionReconciler) reconcileResourceGraphDefinitionGraph(_ context.Context, rgd *v1alpha1.ResourceGraphDefinition) (*graph.Graph, []v1alpha1.ResourceInformation, error) {
	processedRGD, err := r.rgBuilder.NewResourceGraphDefinition(rgd)
	if err != nil {
		return nil, nil, newGraphError(err)
	}

	resourcesInfo := make([]v1alpha1.ResourceInformation, 0, len(processedRGD.Resources))
	for name, resource := range processedRGD.Resources {
		deps := resource.GetDependencies()
		if len(deps) > 0 {
			resourcesInfo = append(resourcesInfo, buildResourceInfo(name, deps))
		}
	}

	return processedRGD, resourcesInfo, nil
}

// buildResourceInfo creates a ResourceInformation struct from name and dependencies
func buildResourceInfo(name string, deps []string) v1alpha1.ResourceInformation {
	dependencies := make([]v1alpha1.Dependency, 0, len(deps))
	for _, dep := range deps {
		dependencies = append(dependencies, v1alpha1.Dependency{ID: dep})
	}
	return v1alpha1.ResourceInformation{
		ID:           name,
		Dependencies: dependencies,
	}
}

// reconcileResourceGraphDefinitionCRD ensures the CRD is present and up to date in the cluster
func (r *ResourceGraphDefinitionReconciler) reconcileResourceGraphDefinitionCRD(ctx context.Context, crd *v1.CustomResourceDefinition) error {
	if err := r.crdManager.Ensure(ctx, *crd); err != nil {
		return newCRDError(err)
	}
	return nil
}

// reconcileResourceGraphDefinitionMicroController starts the microcontroller for handling the resources
func (r *ResourceGraphDefinitionReconciler) reconcileResourceGraphDefinitionMicroController(ctx context.Context, gvr *schema.GroupVersionResource, handler dynamiccontroller.Handler) error {
	err := r.dynamicController.StartServingGVK(ctx, *gvr, handler)
	if err != nil {
		return newMicroControllerError(err)
	}
	return nil
}

// Error types for the resourcegraphdefinition controller
type (
	graphError           struct{ err error }
	crdError             struct{ err error }
	microControllerError struct{ err error }
)

// Error interface implementation
func (e *graphError) Error() string           { return e.err.Error() }
func (e *crdError) Error() string             { return e.err.Error() }
func (e *microControllerError) Error() string { return e.err.Error() }

// Unwrap interface implementation
func (e *graphError) Unwrap() error           { return e.err }
func (e *crdError) Unwrap() error             { return e.err }
func (e *microControllerError) Unwrap() error { return e.err }

// Error constructors
func newGraphError(err error) error           { return &graphError{err} }
func newCRDError(err error) error             { return &crdError{err} }
func newMicroControllerError(err error) error { return &microControllerError{err} }
