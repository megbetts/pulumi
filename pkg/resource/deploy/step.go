// Copyright 2016-2023, Pulumi Corporation.
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

package deploy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pulumi/pulumi/pkg/v3/display"
	"github.com/pulumi/pulumi/pkg/v3/resource/deploy/providers"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag/colors"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/logging"
)

// StepCompleteFunc is the type of functions returned from Step.Apply. These functions are to be called when the engine
// has fully retired a step. You _should not_ modify the resource state in these functions, that will race with the
// snapshot writing code.
type StepCompleteFunc func()

// Step is a specification for a deployment operation.
type Step interface {
	// Apply applies or previews this step. It returns the status of the resource after the step application,
	// a function to call to signal that this step has fully completed, and an error, if one occurred while applying
	// the step.
	//
	// The returned StepCompleteFunc, if not nil, must be called after committing the results of this step into
	// the state of the deployment.
	Apply(preview bool) (resource.Status, StepCompleteFunc, error) // applies or previews this step.

	Op() display.StepOp      // the operation performed by this step.
	URN() resource.URN       // the resource URN (for before and after).
	Type() tokens.Type       // the type affected by this step.
	Provider() string        // the provider reference for this step.
	Old() *resource.State    // the state of the resource before performing this step.
	New() *resource.State    // the state of the resource after performing this step.
	Res() *resource.State    // the latest state for the resource that is known (worst case, old).
	Logical() bool           // true if this step represents a logical operation in the program.
	Deployment() *Deployment // the owning deployment.
}

// SameStep is a mutating step that does nothing.
type SameStep struct {
	deployment *Deployment           // the current deployment.
	reg        RegisterResourceEvent // the registration intent to convey a URN back to.
	old        *resource.State       // the state of the resource before this step.
	new        *resource.State       // the state of the resource after this step.

	// If this is a same-step for a resource being created but which was not --target'ed by the user
	// (and thus was skipped).
	skippedCreate bool
}

var _ Step = (*SameStep)(nil)

func NewSameStep(deployment *Deployment, reg RegisterResourceEvent, old, new *resource.State) Step {
	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(old.URN != "", "old", "must have a URN")
	contract.Requiref(old.ID != "" || !old.Custom, "old", "must have an ID if it is custom")
	contract.Requiref(!old.Custom || old.Provider != "" || providers.IsProviderType(old.Type),
		"old", "must have or be a provider if it is a custom resource")
	contract.Requiref(!old.Delete, "old", "must not be marked for deletion")

	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID == "", "new", "must not have an ID")
	contract.Requiref(!new.Custom || new.Provider != "" || providers.IsProviderType(new.Type),
		"new", "must have or be a provider if it is a custom resource")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")

	return &SameStep{
		deployment: deployment,
		reg:        reg,
		old:        old,
		new:        new,
	}
}

// NewSkippedCreateStep produces a SameStep for a resource that was created but not targeted
// by the user (and thus was skipped). These act as no-op steps (hence 'same') since we are not
// actually creating the resource, but ensure that we complete resource-registration and convey the
// right information downstream. For example, we will not write these into the checkpoint file.
func NewSkippedCreateStep(deployment *Deployment, reg RegisterResourceEvent, new *resource.State) Step {
	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID == "", "new", "must not have an ID")
	contract.Requiref(!new.Custom || new.Provider != "" || providers.IsProviderType(new.Type),
		"new", "must have or be a provider if it is a custom resource")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")

	// Make the old state here a direct copy of the new state
	old := *new
	return &SameStep{
		deployment:    deployment,
		reg:           reg,
		old:           &old,
		new:           new,
		skippedCreate: true,
	}
}

func (s *SameStep) Op() display.StepOp      { return OpSame }
func (s *SameStep) Deployment() *Deployment { return s.deployment }
func (s *SameStep) Type() tokens.Type       { return s.new.Type }
func (s *SameStep) Provider() string        { return s.new.Provider }
func (s *SameStep) URN() resource.URN       { return s.new.URN }
func (s *SameStep) Old() *resource.State    { return s.old }
func (s *SameStep) New() *resource.State    { return s.new }
func (s *SameStep) Res() *resource.State    { return s.new }
func (s *SameStep) Logical() bool           { return true }

func (s *SameStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	// Retain the ID and outputs
	s.new.ID = s.old.ID
	s.new.Outputs = s.old.Outputs

	// If the resource is a provider, ensure that it is present in the registry under the appropriate URNs.
	// We can only do this if the provider is actually a same, not a skipped create.
	if providers.IsProviderType(s.new.Type) && !s.skippedCreate {
		if s.Deployment() != nil {
			err := s.Deployment().SameProvider(s.new)
			if err != nil {
				return resource.StatusOK, nil,
					fmt.Errorf("bad provider state for resource %v: %v", s.URN(), err)
			}
		}
	}

	complete := func() { s.reg.Done(&RegisterResult{State: s.new}) }
	return resource.StatusOK, complete, nil
}

func (s *SameStep) IsSkippedCreate() bool {
	return s.skippedCreate
}

// CreateStep is a mutating step that creates an entirely new resource.
type CreateStep struct {
	deployment    *Deployment                    // the current deployment.
	reg           RegisterResourceEvent          // the registration intent to convey a URN back to.
	old           *resource.State                // the state of the existing resource (only for replacements).
	new           *resource.State                // the state of the resource after this step.
	keys          []resource.PropertyKey         // the keys causing replacement (only for replacements).
	diffs         []resource.PropertyKey         // the keys causing a diff (only for replacements).
	detailedDiff  map[string]plugin.PropertyDiff // the structured property diff (only for replacements).
	replacing     bool                           // true if this is a create due to a replacement.
	pendingDelete bool                           // true if this replacement should create a pending delete.
}

var _ Step = (*CreateStep)(nil)

func NewCreateStep(deployment *Deployment, reg RegisterResourceEvent, new *resource.State) Step {
	contract.Requiref(reg != nil, "reg", "must not be nil")

	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID == "", "new", "must not have an ID")
	contract.Requiref(!new.Custom || new.Provider != "" || providers.IsProviderType(new.Type),
		"new", "must have or be a provider if it is a custom resource")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")
	contract.Requiref(!new.External, "new", "must not be external")

	return &CreateStep{
		deployment: deployment,
		reg:        reg,
		new:        new,
	}
}

func NewCreateReplacementStep(deployment *Deployment, reg RegisterResourceEvent, old, new *resource.State,
	keys, diffs []resource.PropertyKey, detailedDiff map[string]plugin.PropertyDiff, pendingDelete bool,
) Step {
	contract.Requiref(reg != nil, "reg", "must not be nil")

	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(old.URN != "", "old", "must have a URN")
	contract.Requiref(old.ID != "" || !old.Custom, "old", "must have an ID if it is a custom resource")
	contract.Requiref(!old.Delete, "old", "must not be marked for deletion")

	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID == "", "new", "must not have an ID")
	contract.Requiref(!new.Custom || new.Provider != "" || providers.IsProviderType(new.Type),
		"new", "must have or be a provider if it is a custom resource")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")
	contract.Requiref(!new.External, "new", "must not be external")

	return &CreateStep{
		deployment:    deployment,
		reg:           reg,
		old:           old,
		new:           new,
		keys:          keys,
		diffs:         diffs,
		detailedDiff:  detailedDiff,
		replacing:     true,
		pendingDelete: pendingDelete,
	}
}

func (s *CreateStep) Op() display.StepOp {
	if s.replacing {
		return OpCreateReplacement
	}
	return OpCreate
}
func (s *CreateStep) Deployment() *Deployment                      { return s.deployment }
func (s *CreateStep) Type() tokens.Type                            { return s.new.Type }
func (s *CreateStep) Provider() string                             { return s.new.Provider }
func (s *CreateStep) URN() resource.URN                            { return s.new.URN }
func (s *CreateStep) Old() *resource.State                         { return s.old }
func (s *CreateStep) New() *resource.State                         { return s.new }
func (s *CreateStep) Res() *resource.State                         { return s.new }
func (s *CreateStep) Keys() []resource.PropertyKey                 { return s.keys }
func (s *CreateStep) Diffs() []resource.PropertyKey                { return s.diffs }
func (s *CreateStep) DetailedDiff() map[string]plugin.PropertyDiff { return s.detailedDiff }
func (s *CreateStep) Logical() bool                                { return !s.replacing }

func (s *CreateStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	var resourceError error
	resourceStatus := resource.StatusOK
	if s.new.Custom {
		// Invoke the Create RPC function for this provider:
		prov, err := getProvider(s)
		if err != nil {
			return resource.StatusOK, nil, err
		}

		id, outs, rst, err := prov.Create(s.URN(), s.new.Inputs, s.new.CustomTimeouts.Create, s.deployment.preview)
		if err != nil {
			if rst != resource.StatusPartialFailure {
				return rst, nil, err
			}

			resourceError = err
			resourceStatus = rst

			if initErr, isInitErr := err.(*plugin.InitError); isInitErr {
				s.new.InitErrors = initErr.Reasons
			}
		}

		if !preview && id == "" {
			return resourceStatus, nil, fmt.Errorf("provider did not return an ID from Create")
		}

		// Copy any of the default and output properties on the live object state.
		s.new.ID = id
		s.new.Outputs = outs
	}

	// Create should set the Create and Modified timestamps as the resource state has been created.
	now := time.Now().UTC()
	s.new.Created = &now
	s.new.Modified = &now

	// Mark the old resource as pending deletion if necessary.
	if s.replacing && s.pendingDelete {
		s.old.Delete = true
	}

	complete := func() { s.reg.Done(&RegisterResult{State: s.new}) }
	if resourceError == nil {
		return resourceStatus, complete, nil
	}
	return resourceStatus, complete, resourceError
}

// DeleteStep is a mutating step that deletes an existing resource. If `old` is marked "External",
// DeleteStep is a no-op.
type DeleteStep struct {
	deployment     *Deployment           // the current deployment.
	old            *resource.State       // the state of the existing resource.
	replacing      bool                  // true if part of a replacement.
	otherDeletions map[resource.URN]bool // other resources that are planned to delete
}

var _ Step = (*DeleteStep)(nil)

func NewDeleteStep(deployment *Deployment, otherDeletions map[resource.URN]bool, old *resource.State) Step {
	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(old.URN != "", "old", "must have a URN")
	contract.Requiref(old.ID != "" || !old.Custom, "old", "must have an ID if it is a custom resource")
	contract.Requiref(!old.Custom || old.Provider != "" || providers.IsProviderType(old.Type),
		"old", "must have or be a provider if it is a custom resource")
	contract.Requiref(otherDeletions != nil, "otherDeletions", "must not be nil")
	return &DeleteStep{
		deployment:     deployment,
		old:            old,
		otherDeletions: otherDeletions,
	}
}

func NewDeleteReplacementStep(
	deployment *Deployment,
	otherDeletions map[resource.URN]bool,
	old *resource.State,
	pendingReplace bool,
) Step {
	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(old.URN != "", "old", "must have a URN")
	contract.Requiref(old.ID != "" || !old.Custom, "old", "must have an ID if it is a custom resource")
	contract.Requiref(!old.Custom || old.Provider != "" || providers.IsProviderType(old.Type),
		"old", "must have or be a provider if it is a custom resource")

	contract.Requiref(otherDeletions != nil, "otherDeletions", "must not be nil")

	// There are two cases in which we create a delete-replacment step:
	//
	//   1. When creating the delete steps that occur due to a delete-before-replace
	//   2. When creating the delete step that occurs due to a delete-after-replace
	//
	// In the former case, the persistence layer may require that the resource remain in the
	// checkpoint file for purposes of checkpoint integrity. We communicate this case by means
	// of the `PendingReplacement` field on `resource.State`, which we set here.
	//
	// In the latter case, the resource must be deleted, but the deletion may not occur if an earlier step fails.
	// The engine requires that the fact that the old resource must be deleted is persisted in the checkpoint so
	// that it can issue a deletion of this resource on the next update to this stack.
	contract.Assertf(pendingReplace != old.Delete,
		"resource %v cannot be pending replacement and deletion at the same time", old.URN)
	old.PendingReplacement = pendingReplace
	return &DeleteStep{
		deployment:     deployment,
		otherDeletions: otherDeletions,
		old:            old,
		replacing:      true,
	}
}

func (s *DeleteStep) Op() display.StepOp {
	if s.old.External {
		if s.replacing {
			return OpDiscardReplaced
		}
		return OpReadDiscard
	}

	if s.replacing {
		return OpDeleteReplaced
	}
	return OpDelete
}
func (s *DeleteStep) Deployment() *Deployment { return s.deployment }
func (s *DeleteStep) Type() tokens.Type       { return s.old.Type }
func (s *DeleteStep) Provider() string        { return s.old.Provider }
func (s *DeleteStep) URN() resource.URN       { return s.old.URN }
func (s *DeleteStep) Old() *resource.State    { return s.old }
func (s *DeleteStep) New() *resource.State    { return nil }
func (s *DeleteStep) Res() *resource.State    { return s.old }
func (s *DeleteStep) Logical() bool           { return !s.replacing }

func isDeletedWith(with resource.URN, otherDeletions map[resource.URN]bool) bool {
	if with == "" {
		return false
	}
	r, ok := otherDeletions[with]
	if !ok {
		return false
	}
	return r
}

type deleteProtectedError struct {
	urn resource.URN
}

func (d deleteProtectedError) Error() string {
	return fmt.Sprintf("resource %[1]q cannot be deleted\n"+
		"because it is protected. To unprotect the resource, "+
		"either remove the `protect` flag from the resource in your Pulumi "+
		"program and run `pulumi up`, or use the command:\n"+
		"`pulumi state unprotect %[2]s`", d.urn, d.urn.Quote())
}

func (s *DeleteStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	// Refuse to delete protected resources (unless we're replacing them in
	// which case we will of checked protect elsewhere)
	if !s.replacing && s.old.Protect {
		return resource.StatusOK, nil, deleteProtectedError{urn: s.old.URN}
	}

	if preview {
		// Do nothing in preview
	} else if s.old.External {
		// Deleting an External resource is a no-op, since Pulumi does not own the lifecycle.
	} else if s.old.RetainOnDelete {
		// Deleting a "drop on delete" is a no-op as the user has explicitly asked us to not delete the resource.
	} else if isDeletedWith(s.old.DeletedWith, s.otherDeletions) {
		// No need to delete this resource since this resource will be deleted by the another deletion
	} else if s.old.Custom {
		// Not preview and not external and not Drop and is custom, do the actual delete

		// Invoke the Delete RPC function for this provider:
		prov, err := getProvider(s)
		if err != nil {
			return resource.StatusOK, nil, err
		}

		if rst, err := prov.Delete(s.URN(), s.old.ID, s.old.Inputs, s.old.Outputs, s.old.CustomTimeouts.Delete); err != nil {
			return rst, nil, err
		}
	}

	return resource.StatusOK, func() {}, nil
}

type RemovePendingReplaceStep struct {
	deployment *Deployment     // the current deployment.
	old        *resource.State // the state of the existing resource.
}

func NewRemovePendingReplaceStep(deployment *Deployment, old *resource.State) Step {
	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(old.PendingReplacement, "old", "must be pending replacement")
	return &RemovePendingReplaceStep{
		deployment: deployment,
		old:        old,
	}
}

func (s *RemovePendingReplaceStep) Op() display.StepOp {
	return OpRemovePendingReplace
}
func (s *RemovePendingReplaceStep) Deployment() *Deployment { return s.deployment }
func (s *RemovePendingReplaceStep) Type() tokens.Type       { return s.old.Type }
func (s *RemovePendingReplaceStep) Provider() string        { return s.old.Provider }
func (s *RemovePendingReplaceStep) URN() resource.URN       { return s.old.URN }
func (s *RemovePendingReplaceStep) Old() *resource.State    { return s.old }
func (s *RemovePendingReplaceStep) New() *resource.State    { return nil }
func (s *RemovePendingReplaceStep) Res() *resource.State    { return s.old }
func (s *RemovePendingReplaceStep) Logical() bool           { return false }

func (s *RemovePendingReplaceStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	return resource.StatusOK, nil, nil
}

// UpdateStep is a mutating step that updates an existing resource's state.
type UpdateStep struct {
	deployment    *Deployment                    // the current deployment.
	reg           RegisterResourceEvent          // the registration intent to convey a URN back to.
	old           *resource.State                // the state of the existing resource.
	new           *resource.State                // the newly computed state of the resource after updating.
	stables       []resource.PropertyKey         // an optional list of properties that won't change during this update.
	diffs         []resource.PropertyKey         // the keys causing a diff.
	detailedDiff  map[string]plugin.PropertyDiff // the structured diff.
	ignoreChanges []string                       // a list of property paths to ignore when updating.
}

var _ Step = (*UpdateStep)(nil)

func NewUpdateStep(deployment *Deployment, reg RegisterResourceEvent, old, new *resource.State,
	stables, diffs []resource.PropertyKey, detailedDiff map[string]plugin.PropertyDiff,
	ignoreChanges []string,
) Step {
	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(old.URN != "", "old", "must have a URN")
	contract.Requiref(old.ID != "" || !old.Custom, "old", "must have an ID if it is a custom resource")
	contract.Requiref(!old.Custom || old.Provider != "" || providers.IsProviderType(old.Type),
		"old", "must have or be a provider if it is a custom resource")
	contract.Requiref(!old.Delete, "old", "must not be marked for deletion")
	contract.Requiref(!old.External, "old", "must not be an external resource")

	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID == "", "new", "must not have an ID")
	contract.Requiref(!new.Custom || new.Provider != "" || providers.IsProviderType(new.Type),
		"new", "must have or be a provider if it is a custom resource")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")
	contract.Requiref(!new.External, "new", "must not be an external resource")

	return &UpdateStep{
		deployment:    deployment,
		reg:           reg,
		old:           old,
		new:           new,
		stables:       stables,
		diffs:         diffs,
		detailedDiff:  detailedDiff,
		ignoreChanges: ignoreChanges,
	}
}

func (s *UpdateStep) Op() display.StepOp                           { return OpUpdate }
func (s *UpdateStep) Deployment() *Deployment                      { return s.deployment }
func (s *UpdateStep) Type() tokens.Type                            { return s.new.Type }
func (s *UpdateStep) Provider() string                             { return s.new.Provider }
func (s *UpdateStep) URN() resource.URN                            { return s.new.URN }
func (s *UpdateStep) Old() *resource.State                         { return s.old }
func (s *UpdateStep) New() *resource.State                         { return s.new }
func (s *UpdateStep) Res() *resource.State                         { return s.new }
func (s *UpdateStep) Logical() bool                                { return true }
func (s *UpdateStep) Diffs() []resource.PropertyKey                { return s.diffs }
func (s *UpdateStep) DetailedDiff() map[string]plugin.PropertyDiff { return s.detailedDiff }

func (s *UpdateStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	// Always propagate the ID and timestamps even in previews and refreshes.
	s.new.ID = s.old.ID
	s.new.Created = s.old.Created
	s.new.Modified = s.old.Modified

	var resourceError error
	resourceStatus := resource.StatusOK
	if s.new.Custom {
		// Invoke the Update RPC function for this provider:
		prov, err := getProvider(s)
		if err != nil {
			return resource.StatusOK, nil, err
		}

		// Update to the combination of the old "all" state, but overwritten with new inputs.
		outs, rst, upderr := prov.Update(s.URN(), s.old.ID, s.old.Inputs, s.old.Outputs, s.new.Inputs,
			s.new.CustomTimeouts.Update, s.ignoreChanges, s.deployment.preview)
		if upderr != nil {
			if rst != resource.StatusPartialFailure {
				return rst, nil, upderr
			}

			resourceError = upderr
			resourceStatus = rst

			if initErr, isInitErr := upderr.(*plugin.InitError); isInitErr {
				s.new.InitErrors = initErr.Reasons
			}
		}

		// Now copy any output state back in case the update triggered cascading updates to other properties.
		s.new.Outputs = outs

		// UpdateStep doesn't create, but does modify state.
		// Change the Modified timestamp.
		now := time.Now().UTC()
		s.new.Modified = &now
	}

	// Finally, mark this operation as complete.
	complete := func() { s.reg.Done(&RegisterResult{State: s.new}) }
	if resourceError == nil {
		return resourceStatus, complete, nil
	}
	return resourceStatus, complete, resourceError
}

// ReplaceStep is a logical step indicating a resource will be replaced.  This is comprised of three physical steps:
// a creation of the new resource, any number of intervening updates of dependents to the new resource, and then
// a deletion of the now-replaced old resource.  This logical step is primarily here for tools and visualization.
type ReplaceStep struct {
	deployment    *Deployment                    // the current deployment.
	old           *resource.State                // the state of the existing resource.
	new           *resource.State                // the new state snapshot.
	keys          []resource.PropertyKey         // the keys causing replacement.
	diffs         []resource.PropertyKey         // the keys causing a diff.
	detailedDiff  map[string]plugin.PropertyDiff // the structured property diff.
	pendingDelete bool                           // true if a pending deletion should happen.
}

var _ Step = (*ReplaceStep)(nil)

func NewReplaceStep(deployment *Deployment, old, new *resource.State, keys, diffs []resource.PropertyKey,
	detailedDiff map[string]plugin.PropertyDiff, pendingDelete bool,
) Step {
	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(old.URN != "", "old", "must have a URN")
	contract.Requiref(old.ID != "" || !old.Custom, "old", "must have an ID if it is a custom resource")
	contract.Requiref(!old.Delete, "old", "must not be marked for deletion")

	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	// contract.Assert(new.ID == "")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")
	return &ReplaceStep{
		deployment:    deployment,
		old:           old,
		new:           new,
		keys:          keys,
		diffs:         diffs,
		detailedDiff:  detailedDiff,
		pendingDelete: pendingDelete,
	}
}

func (s *ReplaceStep) Op() display.StepOp                           { return OpReplace }
func (s *ReplaceStep) Deployment() *Deployment                      { return s.deployment }
func (s *ReplaceStep) Type() tokens.Type                            { return s.new.Type }
func (s *ReplaceStep) Provider() string                             { return s.new.Provider }
func (s *ReplaceStep) URN() resource.URN                            { return s.new.URN }
func (s *ReplaceStep) Old() *resource.State                         { return s.old }
func (s *ReplaceStep) New() *resource.State                         { return s.new }
func (s *ReplaceStep) Res() *resource.State                         { return s.new }
func (s *ReplaceStep) Keys() []resource.PropertyKey                 { return s.keys }
func (s *ReplaceStep) Diffs() []resource.PropertyKey                { return s.diffs }
func (s *ReplaceStep) DetailedDiff() map[string]plugin.PropertyDiff { return s.detailedDiff }
func (s *ReplaceStep) Logical() bool                                { return true }

func (s *ReplaceStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	// If this is a pending delete, we should have marked the old resource for deletion in the CreateReplacement step.
	contract.Assertf(!s.pendingDelete || s.old.Delete,
		"old resource %v should be marked for deletion if pending delete", s.old.URN)
	return resource.StatusOK, func() {}, nil
}

// ReadStep is a step indicating that an existing resources will be "read" and projected into the Pulumi object
// model. Resources that are read are marked with the "External" bit which indicates to the engine that it does
// not own this resource's lifeycle.
//
// A resource with a given URN can transition freely between an "external" state and a non-external state. If
// a URN that was previously marked "External" (i.e. was the target of a ReadStep in a previous deployment) is the
// target of a RegisterResource in the next deployment, a CreateReplacement step will be issued to indicate the
// transition from external to owned. If a URN that was previously not marked "External" is the target of a
// ReadResource in the next deployment, a ReadReplacement step will be issued to indicate the transition from owned to
// external.
type ReadStep struct {
	deployment *Deployment       // the deployment that produced this read
	event      ReadResourceEvent // the event that should be signaled upon completion
	old        *resource.State   // the old resource state, if one exists for this urn
	new        *resource.State   // the new resource state, to be used to query the provider
	replacing  bool              // whether or not the new resource is replacing the old resource
}

// NewReadStep creates a new Read step.
func NewReadStep(deployment *Deployment, event ReadResourceEvent, old, new *resource.State) Step {
	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID != "", "new", "must have an ID")
	contract.Requiref(new.External, "new", "must be marked as external")
	contract.Requiref(new.Custom, "new", "must be a custom resource")

	// If Old was given, it's either an external resource or its ID is equal to the
	// ID that we are preparing to read.
	if old != nil {
		contract.Requiref(old.ID == new.ID || old.External,
			"old", "must have the same ID as new or be external")
	}

	return &ReadStep{
		deployment: deployment,
		event:      event,
		old:        old,
		new:        new,
		replacing:  false,
	}
}

// NewReadReplacementStep creates a new Read step with the `replacing` flag set. When executed,
// it will pend deletion of the "old" resource, which must not be an external resource.
func NewReadReplacementStep(deployment *Deployment, event ReadResourceEvent, old, new *resource.State) Step {
	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID != "", "new", "must have an ID")
	contract.Requiref(new.External, "new", "must be marked as external")
	contract.Requiref(new.Custom, "new", "must be a custom resource")

	contract.Requiref(old != nil, "old", "must not be nil")
	contract.Requiref(!old.External, "old", "must not be marked as external")

	return &ReadStep{
		deployment: deployment,
		event:      event,
		old:        old,
		new:        new,
		replacing:  true,
	}
}

func (s *ReadStep) Op() display.StepOp {
	if s.replacing {
		return OpReadReplacement
	}

	return OpRead
}

func (s *ReadStep) Deployment() *Deployment { return s.deployment }
func (s *ReadStep) Type() tokens.Type       { return s.new.Type }
func (s *ReadStep) Provider() string        { return s.new.Provider }
func (s *ReadStep) URN() resource.URN       { return s.new.URN }
func (s *ReadStep) Old() *resource.State    { return s.old }
func (s *ReadStep) New() *resource.State    { return s.new }
func (s *ReadStep) Res() *resource.State    { return s.new }
func (s *ReadStep) Logical() bool           { return !s.replacing }

func (s *ReadStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	urn := s.new.URN
	id := s.new.ID

	var resourceError error
	resourceStatus := resource.StatusOK
	// Unlike most steps, Read steps run during previews. The only time
	// we can't run is if the ID we are given is unknown.
	if id == plugin.UnknownStringValue {
		s.new.Outputs = resource.PropertyMap{}
	} else {
		prov, err := getProvider(s)
		if err != nil {
			return resource.StatusOK, nil, err
		}

		result, rst, err := prov.Read(urn, id, nil, s.new.Inputs)
		if err != nil {
			if rst != resource.StatusPartialFailure {
				return rst, nil, err
			}

			resourceError = err
			resourceStatus = rst

			if initErr, isInitErr := err.(*plugin.InitError); isInitErr {
				s.new.InitErrors = initErr.Reasons
			}
		}

		// If there is no such resource, return an error indicating as such.
		if result.Outputs == nil {
			return resource.StatusOK, nil, fmt.Errorf("resource '%s' does not exist", id)
		}
		s.new.Outputs = result.Outputs

		if result.ID != "" {
			s.new.ID = result.ID
		}
	}

	// If we were asked to replace an existing, non-External resource, pend the
	// deletion here.
	if s.replacing {
		s.old.Delete = true
	}
	// Propagate timestamps on Read.
	if s.old != nil {
		s.new.Created = s.old.Created
		s.new.Modified = s.old.Modified
	}
	var inputsChange, outputsChange bool
	if s.old != nil {
		inputsChange = !s.new.Inputs.DeepEquals(s.old.Inputs)
		outputsChange = !s.new.Outputs.DeepEquals(s.old.Outputs)
	}
	// Only update the Modified timestamp if read provides new values that differ
	// from the old state.
	if inputsChange || outputsChange {
		now := time.Now().UTC()
		s.new.Modified = &now
	}

	complete := func() { s.event.Done(&ReadResult{State: s.new}) }
	if resourceError == nil {
		return resourceStatus, complete, nil
	}
	return resourceStatus, complete, resourceError
}

// RefreshStep is a step used to track the progress of a refresh operation. A refresh operation updates the an existing
// resource by reading its current state from its provider plugin. These steps are not issued by the step generator;
// instead, they are issued by the deployment executor as the optional first step in deployment execution.
type RefreshStep struct {
	deployment *Deployment     // the deployment that produced this refresh
	old        *resource.State // the old resource state, if one exists for this urn
	new        *resource.State // the new resource state, to be used to query the provider
	done       chan<- bool     // the channel to use to signal completion, if any
}

// NewRefreshStep creates a new Refresh step.
func NewRefreshStep(deployment *Deployment, old *resource.State, done chan<- bool) Step {
	contract.Requiref(old != nil, "old", "must not be nil")

	// NOTE: we set the new state to the old state by default so that we don't interpret step failures as deletes.
	return &RefreshStep{
		deployment: deployment,
		old:        old,
		new:        old,
		done:       done,
	}
}

func (s *RefreshStep) Op() display.StepOp      { return OpRefresh }
func (s *RefreshStep) Deployment() *Deployment { return s.deployment }
func (s *RefreshStep) Type() tokens.Type       { return s.old.Type }
func (s *RefreshStep) Provider() string        { return s.old.Provider }
func (s *RefreshStep) URN() resource.URN       { return s.old.URN }
func (s *RefreshStep) Old() *resource.State    { return s.old }
func (s *RefreshStep) New() *resource.State    { return s.new }
func (s *RefreshStep) Res() *resource.State    { return s.old }
func (s *RefreshStep) Logical() bool           { return false }

// ResultOp returns the operation that corresponds to the change to this resource after reading its current state, if
// any.
func (s *RefreshStep) ResultOp() display.StepOp {
	if s.new == nil {
		return OpDelete
	}
	if s.new == s.old || s.old.Outputs.Diff(s.new.Outputs) == nil {
		return OpSame
	}
	return OpUpdate
}

func (s *RefreshStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	var complete func()
	if s.done != nil {
		complete = func() { close(s.done) }
	}

	resourceID := s.old.ID

	// Component, provider, and pending-replace resources never change with a refresh; just return the current state.
	if !s.old.Custom || providers.IsProviderType(s.old.Type) || s.old.PendingReplacement {
		return resource.StatusOK, complete, nil
	}

	// For a custom resource, fetch the resource's provider and read the resource's current state.
	prov, err := getProvider(s)
	if err != nil {
		return resource.StatusOK, nil, err
	}

	var initErrors []string
	refreshed, rst, err := prov.Read(s.old.URN, resourceID, s.old.Inputs, s.old.Outputs)
	if err != nil {
		if rst != resource.StatusPartialFailure {
			return rst, nil, err
		}
		if initErr, isInitErr := err.(*plugin.InitError); isInitErr {
			initErrors = initErr.Reasons

			// Partial failure SHOULD NOT cause refresh to fail. Instead:
			//
			// 1. Warn instead that during refresh we noticed the resource has become unhealthy.
			// 2. Make sure the initialization errors are persisted in the state, so that the next
			//    `pulumi up` will surface them to the user.
			err = nil
			msg := fmt.Sprintf("Refreshed resource is in an unhealthy state:\n* %s", strings.Join(initErrors, "\n* "))
			s.Deployment().Diag().Warningf(diag.RawMessage(s.URN(), msg))
		}
	}
	outputs := refreshed.Outputs

	// If the provider specified new inputs for this resource, pick them up now. Otherwise, retain the current inputs.
	inputs := s.old.Inputs
	if refreshed.Inputs != nil {
		inputs = refreshed.Inputs
	}

	if outputs != nil {
		// There is a chance that the ID has changed. We want to allow this change to happen
		// it will have changed already in the outputs, but we need to persist this change
		// at a state level because the Id
		if refreshed.ID != "" && refreshed.ID != resourceID {
			logging.V(7).Infof("Refreshing ID; oldId=%s, newId=%s", resourceID, refreshed.ID)
			resourceID = refreshed.ID
		}

		s.new = resource.NewState(s.old.Type, s.old.URN, s.old.Custom, s.old.Delete, resourceID, inputs, outputs,
			s.old.Parent, s.old.Protect, s.old.External, s.old.Dependencies, initErrors, s.old.Provider,
			s.old.PropertyDependencies, s.old.PendingReplacement, s.old.AdditionalSecretOutputs, s.old.Aliases,
			&s.old.CustomTimeouts, s.old.ImportID, s.old.RetainOnDelete, s.old.DeletedWith, s.old.Created, s.old.Modified,
			s.old.SourcePosition,
		)
		var inputsChange, outputsChange bool
		if s.old != nil {
			inputsChange = !refreshed.Inputs.DeepEquals(s.old.Inputs)
			outputsChange = !refreshed.Outputs.DeepEquals(s.old.Outputs)
		}

		// Only update the Modified timestamp if refresh provides new values that differ
		// from the old state.
		if inputsChange || outputsChange {
			// The refresh has identified an incongruence between the provider and state
			// updated the Modified timestamp to track this.
			now := time.Now().UTC()
			s.new.Modified = &now
		}
	} else {
		s.new = nil
	}

	return rst, nil, err
}

type ImportStep struct {
	deployment    *Deployment                    // the current deployment.
	reg           RegisterResourceEvent          // the registration intent to convey a URN back to.
	original      *resource.State                // the original resource, if this is an import-replace.
	old           *resource.State                // the state of the resource fetched from the provider.
	new           *resource.State                // the newly computed state of the resource after importing.
	replacing     bool                           // true if we are replacing a Pulumi-managed resource.
	planned       bool                           // true if this import is from an import deployment.
	diffs         []resource.PropertyKey         // any keys that differed between the user's program and the actual state.
	detailedDiff  map[string]plugin.PropertyDiff // the structured property diff.
	ignoreChanges []string                       // a list of property paths to ignore when updating.
	randomSeed    []byte                         // the random seed to use for Check.
}

func NewImportStep(deployment *Deployment, reg RegisterResourceEvent, new *resource.State,
	ignoreChanges []string, randomSeed []byte,
) Step {
	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID != "", "new", "must have an ID")
	contract.Requiref(new.Custom, "new", "must be a custom resource")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")
	contract.Requiref(!new.External, "new", "must not be external")
	contract.Requiref(randomSeed != nil, "randomSeed", "must not be nil")

	return &ImportStep{
		deployment:    deployment,
		reg:           reg,
		new:           new,
		ignoreChanges: ignoreChanges,
		randomSeed:    randomSeed,
	}
}

func NewImportReplacementStep(deployment *Deployment, reg RegisterResourceEvent, original, new *resource.State,
	ignoreChanges []string, randomSeed []byte,
) Step {
	contract.Requiref(original != nil, "original", "must not be nil")

	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(new.ID != "", "new", "must have an ID")
	contract.Requiref(new.Custom, "new", "must be a custom resource")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")
	contract.Requiref(!new.External, "new", "must not be external")

	contract.Requiref(randomSeed != nil, "randomSeed", "must not be nil")

	return &ImportStep{
		deployment:    deployment,
		reg:           reg,
		original:      original,
		new:           new,
		replacing:     true,
		ignoreChanges: ignoreChanges,
		randomSeed:    randomSeed,
	}
}

func newImportDeploymentStep(deployment *Deployment, new *resource.State, randomSeed []byte) Step {
	contract.Requiref(new != nil, "new", "must not be nil")
	contract.Requiref(new.URN != "", "new", "must have a URN")
	contract.Requiref(!new.Custom || new.ID != "", "new", "must have an ID")
	contract.Requiref(!new.Delete, "new", "must not be marked for deletion")
	contract.Requiref(!new.External, "new", "must not be external")
	contract.Requiref(!new.Custom || randomSeed != nil, "randomSeed", "must not be nil")

	return &ImportStep{
		deployment: deployment,
		reg:        noopEvent(0),
		new:        new,
		planned:    true,
		randomSeed: randomSeed,
	}
}

func (s *ImportStep) Op() display.StepOp {
	if s.replacing {
		return OpImportReplacement
	}
	return OpImport
}

func (s *ImportStep) Deployment() *Deployment                      { return s.deployment }
func (s *ImportStep) Type() tokens.Type                            { return s.new.Type }
func (s *ImportStep) Provider() string                             { return s.new.Provider }
func (s *ImportStep) URN() resource.URN                            { return s.new.URN }
func (s *ImportStep) Old() *resource.State                         { return s.old }
func (s *ImportStep) New() *resource.State                         { return s.new }
func (s *ImportStep) Res() *resource.State                         { return s.new }
func (s *ImportStep) Logical() bool                                { return !s.replacing }
func (s *ImportStep) Diffs() []resource.PropertyKey                { return s.diffs }
func (s *ImportStep) DetailedDiff() map[string]plugin.PropertyDiff { return s.detailedDiff }

func (s *ImportStep) Apply(preview bool) (resource.Status, StepCompleteFunc, error) {
	complete := func() {
		s.reg.Done(&RegisterResult{State: s.new})
	}

	// If this is a planned import, ensure that the resource does not exist in the old state file.
	if s.planned {
		if _, ok := s.deployment.olds[s.new.URN]; ok {
			return resource.StatusOK, nil, fmt.Errorf("resource '%v' already exists", s.new.URN)
		}
		if s.new.Parent.Type() != resource.RootStackType {
			_, ok := s.deployment.news.get(s.new.Parent)
			if !ok {
				return resource.StatusOK, nil, fmt.Errorf("unknown parent '%v' for resource '%v'",
					s.new.Parent, s.new.URN)
			}
		}
	}

	// Only need to do anything here for custom resources, components just import as empty
	inputs := resource.PropertyMap{}
	outputs := resource.PropertyMap{}
	var prov plugin.Provider
	rst := resource.StatusOK
	if s.new.Custom {
		// Read the current state of the resource to import. If the provider does not hand us back any inputs for the
		// resource, it probably needs to be updated. If the resource does not exist at all, fail the import.
		var err error
		prov, err = getProvider(s)
		if err != nil {
			return resource.StatusOK, nil, err
		}
		var read plugin.ReadResult
		read, rst, err = prov.Read(s.new.URN, s.new.ID, nil, nil)
		if err != nil {
			if initErr, isInitErr := err.(*plugin.InitError); isInitErr {
				s.new.InitErrors = initErr.Reasons
			} else {
				return rst, nil, err
			}
		}
		if read.Outputs == nil {
			return rst, nil, fmt.Errorf("resource '%v' does not exist", s.new.ID)
		}
		if read.Inputs == nil {
			return resource.StatusOK, nil,
				fmt.Errorf("provider does not support importing resources; please try updating the '%v' plugin",
					s.new.URN.Type().Package())
		}
		if read.ID != "" {
			s.new.ID = read.ID
		}
		inputs = read.Inputs
		outputs = read.Outputs
	}

	s.new.Outputs = outputs
	// Magic up an old state so the frontend can display a proper diff. This state is the output of the just-executed
	// `Read` combined with the resource identity and metadata from the desired state. This ensures that the only
	// differences between the old and new states are between the inputs and outputs.
	s.old = resource.NewState(s.new.Type, s.new.URN, s.new.Custom, false, s.new.ID, inputs, outputs,
		s.new.Parent, s.new.Protect, false, s.new.Dependencies, s.new.InitErrors, s.new.Provider,
		s.new.PropertyDependencies, false, nil, nil, &s.new.CustomTimeouts, s.new.ImportID, s.new.RetainOnDelete,
		s.new.DeletedWith, nil, nil, s.new.SourcePosition)

	// Import takes a resource that Pulumi did not create and imports it into pulumi state.
	now := time.Now().UTC()
	s.new.Modified = &now
	// Set Created to now as the resource has been created in the state.
	s.new.Created = &now

	// If this is a component we don't need to do the rest of the input validation
	if !s.new.Custom {
		return rst, complete, nil
	}

	// If this step came from an import deployment, we need to fetch any required inputs from the state.
	if s.planned {
		contract.Assertf(len(s.new.Inputs) == 0, "import resource cannot have existing inputs")

		// Get the import object and see if it had properties set
		var inputProperties []string
		for _, imp := range s.deployment.imports {
			if imp.ID == s.old.ID {
				inputProperties = imp.Properties
				break
			}
		}

		if len(inputProperties) == 0 {
			logging.V(9).Infof("Importing %v with all properties", s.URN())
			s.new.Inputs = s.old.Inputs.Copy()
		} else {
			logging.V(9).Infof("Importing %v with supplied properties: %v", s.URN(), inputProperties)
			for _, p := range inputProperties {
				k := resource.PropertyKey(p)
				if value, has := s.old.Inputs[k]; has {
					s.new.Inputs[k] = value
				}
			}
		}

		// Check the provider inputs for consistency. If the inputs fail validation, the import will still succeed, but
		// we will display the validation failures and a message informing the user that the failures are almost
		// definitely a provider bug.
		_, failures, err := prov.Check(s.new.URN, s.old.Inputs, s.new.Inputs, preview, s.randomSeed)
		if err != nil {
			return rst, nil, err
		}

		// Print this warning before printing all the check failures to give better context.
		if len(failures) != 0 {

			// Based on if the user passed 'properties' or not we want to change the error message here.
			var errorMessage string
			if len(inputProperties) == 0 {
				ref, err := providers.ParseReference(s.Provider())
				contract.AssertNoErrorf(err, "failed to parse provider reference %q", s.Provider())

				pkgName := ref.URN().Type().Name()
				errorMessage = fmt.Sprintf("This is almost certainly a bug in the `%s` provider.", pkgName)
			} else {
				errorMessage = "Try specifying a different set of properties to import with in the future."
			}

			s.deployment.Diag().Warningf(diag.Message(s.new.URN,
				"One or more imported inputs failed to validate. %s "+
					"The import will still proceed, but you will need to edit the generated code after copying it into your program."),
				errorMessage)
		}

		issueCheckFailures(s.deployment.Diag().Warningf, s.new, s.new.URN, failures)

		s.diffs, s.detailedDiff = []resource.PropertyKey{}, map[string]plugin.PropertyDiff{}

		return rst, complete, nil
	}

	// Set inputs back to their old values (if any) for any "ignored" properties
	processedInputs, err := processIgnoreChanges(s.new.Inputs, s.old.Inputs, s.ignoreChanges)
	if err != nil {
		return resource.StatusOK, nil, err
	}
	s.new.Inputs = processedInputs

	// Check the inputs using the provider inputs for defaults.
	inputs, failures, err := prov.Check(s.new.URN, s.old.Inputs, s.new.Inputs, preview, s.randomSeed)
	if err != nil {
		return rst, nil, err
	}
	if issueCheckErrors(s.deployment, s.new, s.new.URN, failures) {
		return rst, nil, errors.New("one or more inputs failed to validate")
	}
	s.new.Inputs = inputs

	// Diff the user inputs against the provider inputs. If there are any differences, fail the import unless this step
	// is from an import deployment.
	diff, err := diffResource(s.new.URN, s.new.ID, s.old.Inputs, s.old.Outputs, s.new.Inputs, prov, preview,
		s.ignoreChanges)
	if err != nil {
		return rst, nil, err
	}

	s.diffs, s.detailedDiff = diff.ChangedKeys, diff.DetailedDiff

	if diff.Changes != plugin.DiffNone {
		const message = "inputs to import do not match the existing resource"

		if preview {
			s.deployment.ctx.Diag.Warningf(diag.StreamMessage(s.new.URN,
				message+"; importing this resource will fail", 0))
		} else {
			err = errors.New(message)
		}
	}

	// If we were asked to replace an existing, non-External resource, pend the deletion here.
	if err == nil && s.replacing {
		s.original.Delete = true
	}

	return rst, complete, err
}

const (
	OpSame                 display.StepOp = "same"                   // nothing to do.
	OpCreate               display.StepOp = "create"                 // creating a new resource.
	OpUpdate               display.StepOp = "update"                 // updating an existing resource.
	OpDelete               display.StepOp = "delete"                 // deleting an existing resource.
	OpReplace              display.StepOp = "replace"                // replacing a resource with a new one.
	OpCreateReplacement    display.StepOp = "create-replacement"     // creating a new resource for a replacement.
	OpDeleteReplaced       display.StepOp = "delete-replaced"        // deleting an existing resource after replacement.
	OpRead                 display.StepOp = "read"                   // reading an existing resource.
	OpReadReplacement      display.StepOp = "read-replacement"       // reading an existing resource for a replacement.
	OpRefresh              display.StepOp = "refresh"                // refreshing an existing resource.
	OpReadDiscard          display.StepOp = "discard"                // removing a resource that was read.
	OpDiscardReplaced      display.StepOp = "discard-replaced"       // discarding a read resource that was replaced.
	OpRemovePendingReplace display.StepOp = "remove-pending-replace" // removing a pending replace resource.
	OpImport               display.StepOp = "import"                 // import an existing resource.
	OpImportReplacement    display.StepOp = "import-replacement"     // replace an existing resource
	// with an imported resource.
)

// StepOps contains the full set of step operation types.
var StepOps = []display.StepOp{
	OpSame,
	OpCreate,
	OpUpdate,
	OpDelete,
	OpReplace,
	OpCreateReplacement,
	OpDeleteReplaced,
	OpRead,
	OpReadReplacement,
	OpRefresh,
	OpReadDiscard,
	OpDiscardReplaced,
	OpRemovePendingReplace,
	OpImport,
	OpImportReplacement,
}

// Color returns a suggested color for lines of this op type.
func Color(op display.StepOp) string {
	switch op {
	case OpSame:
		return colors.SpecUnimportant
	case OpCreate, OpImport:
		return colors.SpecCreate
	case OpDelete:
		return colors.SpecDelete
	case OpUpdate:
		return colors.SpecUpdate
	case OpReplace:
		return colors.SpecReplace
	case OpCreateReplacement:
		return colors.SpecCreateReplacement
	case OpDeleteReplaced:
		return colors.SpecDeleteReplaced
	case OpRead:
		return colors.SpecRead
	case OpReadReplacement, OpImportReplacement:
		return colors.SpecReplace
	case OpRefresh:
		return colors.SpecUpdate
	case OpReadDiscard, OpDiscardReplaced:
		return colors.SpecDelete
	default:
		contract.Failf("Unrecognized resource step op: '%v'", op)
		return ""
	}
}

// ColorProgress returns a suggested coloring for lines of this of type which
// are progressing.
func ColorProgress(op display.StepOp) string {
	return colors.Bold + Color(op)
}

// Prefix returns a suggested prefix for lines of this op type.
func Prefix(op display.StepOp, done bool) string {
	var color string
	if done {
		color = Color(op)
	} else {
		color = ColorProgress(op)
	}
	return color + RawPrefix(op)
}

// RawPrefix returns the uncolorized prefix text.
func RawPrefix(op display.StepOp) string {
	switch op {
	case OpSame:
		return "  "
	case OpCreate:
		return "+ "
	case OpDelete:
		return "- "
	case OpUpdate:
		return "~ "
	case OpReplace:
		return "+-"
	case OpCreateReplacement:
		return "++"
	case OpDeleteReplaced:
		return "--"
	case OpRead:
		return "> "
	case OpReadReplacement:
		return ">>"
	case OpRefresh:
		return "~ "
	case OpReadDiscard:
		return "< "
	case OpDiscardReplaced:
		return "<<"
	case OpImport:
		return "= "
	case OpImportReplacement:
		return "=>"
	default:
		contract.Failf("Unrecognized resource step op: %v", op)
		return ""
	}
}

func PastTense(op display.StepOp) string {
	switch op {
	case OpSame, OpCreate, OpReplace, OpCreateReplacement, OpUpdate, OpReadReplacement:
		return string(op) + "d"
	case OpRefresh:
		return "refreshed"
	case OpRead:
		return "read"
	case OpReadDiscard, OpDiscardReplaced:
		return "discarded"
	case OpDelete, OpDeleteReplaced:
		return "deleted"
	case OpImport, OpImportReplacement:
		return "imported"
	default:
		contract.Failf("Unexpected resource step op: %v", op)
		return ""
	}
}

// Suffix returns a suggested suffix for lines of this op type.
func Suffix(op display.StepOp) string {
	switch op {
	case OpCreateReplacement, OpUpdate, OpReplace, OpReadReplacement, OpRefresh, OpImportReplacement:
		return colors.Reset // updates and replacements colorize individual lines; get has none
	}
	return ""
}

// ConstrainedTo returns true if this operation is no more impactful than the constraint.
func ConstrainedTo(op display.StepOp, constraint display.StepOp) bool {
	var allowed []display.StepOp
	switch constraint {
	case OpSame, OpDelete, OpRead, OpReadReplacement, OpRefresh, OpReadDiscard, OpDiscardReplaced,
		OpRemovePendingReplace, OpImport, OpImportReplacement:
		allowed = []display.StepOp{constraint}
	case OpCreate:
		allowed = []display.StepOp{OpSame, OpCreate}
	case OpUpdate:
		allowed = []display.StepOp{OpSame, OpUpdate}
	case OpReplace, OpCreateReplacement, OpDeleteReplaced:
		allowed = []display.StepOp{OpSame, OpUpdate, constraint}
	}
	for _, candidate := range allowed {
		if candidate == op {
			return true
		}
	}
	return false
}

// getProvider fetches the provider for the given step.
func getProvider(s Step) (plugin.Provider, error) {
	if providers.IsProviderType(s.Type()) {
		return s.Deployment().providers, nil
	}
	ref, err := providers.ParseReference(s.Provider())
	if err != nil {
		return nil, fmt.Errorf("bad provider reference '%v' for resource %v: %v", s.Provider(), s.URN(), err)
	}
	if providers.IsDenyDefaultsProvider(ref) {
		pkg := providers.GetDeniedDefaultProviderPkg(ref)
		msg := diag.GetDefaultProviderDenied(s.URN()).Message
		return nil, fmt.Errorf(msg, pkg, s.URN())
	}
	provider, ok := s.Deployment().GetProvider(ref)
	if !ok {
		return nil, fmt.Errorf("unknown provider '%v' for resource %v", s.Provider(), s.URN())
	}
	return provider, nil
}
