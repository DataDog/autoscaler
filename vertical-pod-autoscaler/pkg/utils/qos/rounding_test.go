package qos

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/poc.autoscaling.k8s.io/v1alpha1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/annotations"
	"reflect"
	"testing"
)

func res(cores float64, mib float64) model.Resources {
	return model.Resources{
		model.ResourceCPU:    model.ResourceAmount(cores * 1000),
		model.ResourceMemory: model.ResourceAmount(mib * 1048576),
	}
}

type extensionBuilder struct {
	extensions annotations.DatadogExtensions
}

func Ext() *extensionBuilder {
	return &extensionBuilder{
		// Default to invalid values with 'Specified' flags false.
		extensions: annotations.DatadogExtensions{
			ObjectTypeSpecified:  false,
			ObjectType:           "",
			UpdateModeSpecified:  false,
			UpdateMode:           "",
			QoSSpecified:         false,
			QoSEnable:            false,
			CoreDivisorSpecified: false,
			CoreDivisor:          0,
			RamPerCoreSpecified:  false,
			RamPerCore: resource.Quantity{
				Format: "",
			},
		},
	}
}
func (b *extensionBuilder) SetQoS(qos bool) *extensionBuilder {
	b.extensions.QoSSpecified = true
	b.extensions.QoSEnable = qos
	return b
}

func (b *extensionBuilder) SetRamPerCore(mib float64) *extensionBuilder {
	b.extensions.RamPerCoreSpecified = true
	b.extensions.RamPerCore = *resource.NewScaledQuantity(int64(mib*1024), resource.Kilo)
	return b
}

func (b *extensionBuilder) SetCoreDivisor(div int) *extensionBuilder {
	b.extensions.CoreDivisorSpecified = true
	b.extensions.CoreDivisor = div
	return b
}

func (b *extensionBuilder) SetUpdateMode(mode v1alpha1.UpdateMode) *extensionBuilder {
	b.extensions.UpdateModeSpecified = true
	b.extensions.UpdateMode = mode
	return b
}

func (b *extensionBuilder) SetObjectType(ty string) *extensionBuilder {
	b.extensions.ObjectTypeSpecified = true
	b.extensions.ObjectType = ty
	return b
}

func (b *extensionBuilder) Build() annotations.DatadogExtensions {
	return b.extensions
}

func TestNeedsQoS(t *testing.T) {
	type args struct {
		datadogExtensions annotations.DatadogExtensions
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{"Deployment", args{Ext().SetObjectType("Deployment").Build()}, true},
		{"StatefulSet", args{Ext().SetObjectType("StatefulSet").Build()}, true},
		{"DaemonSet", args{Ext().SetObjectType("DaemonSet").Build()}, false},
		{"Override", args{Ext().SetObjectType("Deployment").SetQoS(false).Build()}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsQoS(tt.args.datadogExtensions); got != tt.want {
				t.Errorf("NeedsQoS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRoundResourcesToPolicy(t *testing.T) {
	type args struct {
		extensions        annotations.DatadogExtensions
		originalResources model.Resources
	}
	tests := []struct {
		name string
		args args
		want model.Resources
	}{
		{"QoS force-off deployment", args{Ext().SetQoS(false).SetObjectType("Deployment").Build(), res(1.5, 512)}, res(1.5, 512)},
		{"QoS force-on deployment", args{Ext().SetQoS(true).SetObjectType("Deployment").Build(), res(1.5, 512)}, res(2.0, 512)},
		{"QoS no-force deployment", args{Ext().SetObjectType("Deployment").Build(), res(1.5, 512)}, res(2.0, 512)},
		{"QoS no-force daemonset", args{Ext().SetQoS(false).SetObjectType("DaemonSet").Build(), res(1.5, 512)}, res(1.5, 512)},
		{"QoS deployment 4c", args{Ext().SetCoreDivisor(4).SetObjectType("Deployment").Build(), res(1.5, 512)}, res(4.0, 512)},
		{"Non-QoS deployment 4c", args{Ext().SetCoreDivisor(4).SetQoS(false).Build(), res(1.5, 512)}, res(1.5, 512)},
		{"QoS deployment 4c RamPerCore", args{Ext().SetCoreDivisor(4).SetObjectType("Deployment").SetRamPerCore(64).Build(), res(1.5, 512)}, res(8.0, 512)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RoundResourcesToPolicy(tt.args.extensions, tt.args.originalResources); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RoundResourcesToPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_kindNeedsQoS(t *testing.T) {
	type args struct {
		kind string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{"DaemonSet", args{"DaemonSet"}, false},
		{"Deployment", args{"Deployment"}, true},
		{"StatefulSet", args{"StatefulSet"}, true},
		{"OtherThing", args{"OtherThing"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kindNeedsQoS(tt.args.kind); got != tt.want {
				t.Errorf("kindNeedsQoS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_roundCoresToPolicy(t *testing.T) {
	type args struct {
		datadogExtensions annotations.DatadogExtensions
		cores             model.ResourceAmount
	}
	tests := []struct {
		name string
		args args
		want model.ResourceAmount
	}{
		{"Off", args{Ext().SetQoS(false).Build(), model.ResourceAmount(1500)}, model.ResourceAmount(1500)},
		{"Pure rounding", args{Ext().SetQoS(true).Build(), model.ResourceAmount(1500)}, model.ResourceAmount(2000)},
		{"QoS 3c", args{Ext().SetQoS(true).SetCoreDivisor(3).Build(), model.ResourceAmount(1500)}, model.ResourceAmount(3000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roundCoresToPolicy(tt.args.datadogExtensions, tt.args.cores); got != tt.want {
				t.Errorf("roundCoresToPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}
