// Code generated by test DO NOT EDIT.
// *** WARNING: Do not edit by hand unless you're certain you know what you are doing! ***

package local

import (
	"context"
	"reflect"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

type MyEnum float64

const (
	MyEnumPi    = MyEnum(3.1415)
	MyEnumSmall = MyEnum(1e-07)
)

func (MyEnum) ElementType() reflect.Type {
	return reflect.TypeOf((*MyEnum)(nil)).Elem()
}

func (e MyEnum) ToMyEnumOutput() MyEnumOutput {
	return pulumi.ToOutput(e).(MyEnumOutput)
}

func (e MyEnum) ToMyEnumOutputWithContext(ctx context.Context) MyEnumOutput {
	return pulumi.ToOutputWithContext(ctx, e).(MyEnumOutput)
}

func (e MyEnum) ToMyEnumPtrOutput() MyEnumPtrOutput {
	return e.ToMyEnumPtrOutputWithContext(context.Background())
}

func (e MyEnum) ToMyEnumPtrOutputWithContext(ctx context.Context) MyEnumPtrOutput {
	return MyEnum(e).ToMyEnumOutputWithContext(ctx).ToMyEnumPtrOutputWithContext(ctx)
}

func (e MyEnum) ToFloat64Output() pulumi.Float64Output {
	return pulumi.ToOutput(pulumi.Float64(e)).(pulumi.Float64Output)
}

func (e MyEnum) ToFloat64OutputWithContext(ctx context.Context) pulumi.Float64Output {
	return pulumi.ToOutputWithContext(ctx, pulumi.Float64(e)).(pulumi.Float64Output)
}

func (e MyEnum) ToFloat64PtrOutput() pulumi.Float64PtrOutput {
	return pulumi.Float64(e).ToFloat64PtrOutputWithContext(context.Background())
}

func (e MyEnum) ToFloat64PtrOutputWithContext(ctx context.Context) pulumi.Float64PtrOutput {
	return pulumi.Float64(e).ToFloat64OutputWithContext(ctx).ToFloat64PtrOutputWithContext(ctx)
}

type MyEnumOutput struct{ *pulumi.OutputState }

func (MyEnumOutput) ElementType() reflect.Type {
	return reflect.TypeOf((*MyEnum)(nil)).Elem()
}

func (o MyEnumOutput) ToMyEnumOutput() MyEnumOutput {
	return o
}

func (o MyEnumOutput) ToMyEnumOutputWithContext(ctx context.Context) MyEnumOutput {
	return o
}

func (o MyEnumOutput) ToMyEnumPtrOutput() MyEnumPtrOutput {
	return o.ToMyEnumPtrOutputWithContext(context.Background())
}

func (o MyEnumOutput) ToMyEnumPtrOutputWithContext(ctx context.Context) MyEnumPtrOutput {
	return o.ApplyTWithContext(ctx, func(_ context.Context, v MyEnum) *MyEnum {
		return &v
	}).(MyEnumPtrOutput)
}

func (o MyEnumOutput) ToFloat64Output() pulumi.Float64Output {
	return o.ToFloat64OutputWithContext(context.Background())
}

func (o MyEnumOutput) ToFloat64OutputWithContext(ctx context.Context) pulumi.Float64Output {
	return o.ApplyTWithContext(ctx, func(_ context.Context, e MyEnum) float64 {
		return float64(e)
	}).(pulumi.Float64Output)
}

func (o MyEnumOutput) ToFloat64PtrOutput() pulumi.Float64PtrOutput {
	return o.ToFloat64PtrOutputWithContext(context.Background())
}

func (o MyEnumOutput) ToFloat64PtrOutputWithContext(ctx context.Context) pulumi.Float64PtrOutput {
	return o.ApplyTWithContext(ctx, func(_ context.Context, e MyEnum) *float64 {
		v := float64(e)
		return &v
	}).(pulumi.Float64PtrOutput)
}

type MyEnumPtrOutput struct{ *pulumi.OutputState }

func (MyEnumPtrOutput) ElementType() reflect.Type {
	return reflect.TypeOf((**MyEnum)(nil)).Elem()
}

func (o MyEnumPtrOutput) ToMyEnumPtrOutput() MyEnumPtrOutput {
	return o
}

func (o MyEnumPtrOutput) ToMyEnumPtrOutputWithContext(ctx context.Context) MyEnumPtrOutput {
	return o
}

func (o MyEnumPtrOutput) Elem() MyEnumOutput {
	return o.ApplyT(func(v *MyEnum) MyEnum {
		if v != nil {
			return *v
		}
		var ret MyEnum
		return ret
	}).(MyEnumOutput)
}

func (o MyEnumPtrOutput) ToFloat64PtrOutput() pulumi.Float64PtrOutput {
	return o.ToFloat64PtrOutputWithContext(context.Background())
}

func (o MyEnumPtrOutput) ToFloat64PtrOutputWithContext(ctx context.Context) pulumi.Float64PtrOutput {
	return o.ApplyTWithContext(ctx, func(_ context.Context, e *MyEnum) *float64 {
		if e == nil {
			return nil
		}
		v := float64(*e)
		return &v
	}).(pulumi.Float64PtrOutput)
}

// MyEnumInput is an input type that accepts MyEnumArgs and MyEnumOutput values.
// You can construct a concrete instance of `MyEnumInput` via:
//
//	MyEnumArgs{...}
type MyEnumInput interface {
	pulumi.Input

	ToMyEnumOutput() MyEnumOutput
	ToMyEnumOutputWithContext(context.Context) MyEnumOutput
}

var myEnumPtrType = reflect.TypeOf((**MyEnum)(nil)).Elem()

type MyEnumPtrInput interface {
	pulumi.Input

	ToMyEnumPtrOutput() MyEnumPtrOutput
	ToMyEnumPtrOutputWithContext(context.Context) MyEnumPtrOutput
}

type myEnumPtr float64

func MyEnumPtr(v float64) MyEnumPtrInput {
	return (*myEnumPtr)(&v)
}

func (*myEnumPtr) ElementType() reflect.Type {
	return myEnumPtrType
}

func (in *myEnumPtr) ToMyEnumPtrOutput() MyEnumPtrOutput {
	return pulumi.ToOutput(in).(MyEnumPtrOutput)
}

func (in *myEnumPtr) ToMyEnumPtrOutputWithContext(ctx context.Context) MyEnumPtrOutput {
	return pulumi.ToOutputWithContext(ctx, in).(MyEnumPtrOutput)
}

func (in *myEnumPtr) ToOutput(ctx context.Context) pulumix.Output[*MyEnum] {
	return pulumix.Output[*MyEnum]{
		OutputState: in.ToMyEnumPtrOutputWithContext(ctx).OutputState,
	}
}

func init() {
	pulumi.RegisterInputType(reflect.TypeOf((*MyEnumInput)(nil)).Elem(), MyEnum(3.1415))
	pulumi.RegisterInputType(reflect.TypeOf((*MyEnumPtrInput)(nil)).Elem(), MyEnum(3.1415))
	pulumi.RegisterOutputType(MyEnumOutput{})
	pulumi.RegisterOutputType(MyEnumPtrOutput{})
}
