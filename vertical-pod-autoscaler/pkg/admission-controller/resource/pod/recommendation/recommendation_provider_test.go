/*
Copyright 2018 The Kubernetes Authors.

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

package recommendation

import (
	"flag"
	"fmt"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/annotations"
	"math"
	"strconv"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/limitrange"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"

	"github.com/stretchr/testify/assert"
)

func mustParseResourcePointer(val string) *resource.Quantity {
	q := resource.MustParse(val)
	return &q
}

type fakeLimitRangeCalculator struct {
	containerLimitRange *apiv1.LimitRangeItem
	containerErr        error
	podLimitRange       *apiv1.LimitRangeItem
	podErr              error
}

func (nlrc *fakeLimitRangeCalculator) GetContainerLimitRangeItem(namespace string) (*apiv1.LimitRangeItem, error) {
	return nlrc.containerLimitRange, nlrc.containerErr
}

func (nlrc *fakeLimitRangeCalculator) GetPodLimitRangeItem(namespace string) (*apiv1.LimitRangeItem, error) {
	return nlrc.podLimitRange, nlrc.podErr
}

func TestUpdateResourceRequests(t *testing.T) {
	containerName := "container1"
	vpaName := "vpa1"
	labels := map[string]string{"app": "testingApp"}
	vpaBuilder := test.VerticalPodAutoscaler().
		WithName(vpaName).
		WithContainer(containerName).
		WithTarget("2", "200Mi").
		WithMinAllowed("1", "100Mi").
		WithMaxAllowed("3", "1Gi")
	vpa := vpaBuilder.Get()

	uninitialized := test.Pod().WithName("test_uninitialized").
		AddContainer(test.Container().WithName(containerName).Get()).
		WithLabels(labels).Get()

	initializedContainer := test.Container().WithName(containerName).
		WithCPURequest(resource.MustParse("2")).WithMemRequest(resource.MustParse("100Mi")).Get()
	initialized := test.Pod().WithName("test_initialized").
		AddContainer(initializedContainer).WithLabels(labels).Get()

	limitsMatchRequestsContainer := test.Container().WithName(containerName).
		WithCPURequest(resource.MustParse("2")).WithCPULimit(resource.MustParse("2")).
		WithMemRequest(resource.MustParse("200Mi")).WithMemLimit(resource.MustParse("200Mi")).Get()
	limitsMatchRequestsPod := test.Pod().WithName("test_initialized").
		AddContainer(limitsMatchRequestsContainer).WithLabels(labels).Get()

	containerWithDoubleLimit := test.Container().WithName(containerName).
		WithCPURequest(resource.MustParse("1")).WithCPULimit(resource.MustParse("2")).
		WithMemRequest(resource.MustParse("100Mi")).WithMemLimit(resource.MustParse("200Mi")).Get()
	podWithDoubleLimit := test.Pod().WithName("test_initialized").
		AddContainer(containerWithDoubleLimit).WithLabels(labels).Get()

	containerWithTenfoldLimit := test.Container().WithName(containerName).
		WithCPURequest(resource.MustParse("1")).WithCPULimit(resource.MustParse("10")).
		WithMemRequest(resource.MustParse("100Mi")).WithMemLimit(resource.MustParse("1000Mi")).Get()
	podWithTenfoldLimit := test.Pod().WithName("test_initialized").
		AddContainer(containerWithTenfoldLimit).WithLabels(labels).Get()

	limitsNoRequestsContainer := test.Container().WithName(containerName).
		WithCPULimit(resource.MustParse("2")).WithMemLimit(resource.MustParse("200Mi")).Get()
	limitsNoRequestsPod := test.Pod().WithName("test_initialized").
		AddContainer(limitsNoRequestsContainer).WithLabels(labels).Get()

	targetBelowMinVPA := vpaBuilder.WithTarget("3", "150Mi").WithMinAllowed("4", "300Mi").WithMaxAllowed("5", "1Gi").Get()
	targetAboveMaxVPA := vpaBuilder.WithTarget("7", "2Gi").WithMinAllowed("4", "300Mi").WithMaxAllowed("5", "1Gi").Get()
	vpaWithHighMemory := vpaBuilder.WithTarget("2", "1000Mi").WithMaxAllowed("3", "3Gi").Get()
	vpaWithExabyteRecommendation := vpaBuilder.WithTarget("1Ei", "1Ei").WithMaxAllowed("1Ei", "1Ei").Get()

	resourceRequestsAndLimitsVPA := vpaBuilder.WithControlledValues(vpa_types.ContainerControlledValuesRequestsAndLimits).Get()
	resourceRequestsOnlyVPA := vpaBuilder.WithControlledValues(vpa_types.ContainerControlledValuesRequestsOnly).Get()
	resourceRequestsOnlyVPAHighTarget := vpaBuilder.WithControlledValues(vpa_types.ContainerControlledValuesRequestsOnly).
		WithTarget("3", "500Mi").WithMaxAllowed("5", "1Gi").Get()

	vpaWithEmptyRecommendation := vpaBuilder.Get()
	vpaWithEmptyRecommendation.Status.Recommendation = &vpa_types.RecommendedPodResources{}
	vpaWithNilRecommendation := vpaBuilder.Get()
	vpaWithNilRecommendation.Status.Recommendation = nil

	testCases := []struct {
		name              string
		pod               *apiv1.Pod
		vpa               *vpa_types.VerticalPodAutoscaler
		expectedAction    bool
		expectedError     error
		expectedMem       resource.Quantity
		expectedCPU       resource.Quantity
		expectedCPULimit  *resource.Quantity
		expectedMemLimit  *resource.Quantity
		limitRange        *apiv1.LimitRangeItem
		limitRangeCalcErr error
		annotations       vpa_api_util.ContainerToAnnotationsMap
	}{
		{
			name:           "uninitialized pod",
			pod:            uninitialized,
			vpa:            vpa,
			expectedAction: true,
			expectedMem:    resource.MustParse("200Mi"),
			expectedCPU:    resource.MustParse("2"),
		},
		{
			name:           "target below min",
			pod:            uninitialized,
			vpa:            targetBelowMinVPA,
			expectedAction: true,
			expectedMem:    resource.MustParse("300Mi"), // MinMemory is expected to be used
			expectedCPU:    resource.MustParse("4"),     // MinCpu is expected to be used
			annotations: vpa_api_util.ContainerToAnnotationsMap{
				containerName: []string{"cpu capped to minAllowed", "memory capped to minAllowed"},
			},
		},
		{
			name:           "target above max",
			pod:            uninitialized,
			vpa:            targetAboveMaxVPA,
			expectedAction: true,
			expectedMem:    resource.MustParse("1Gi"), // MaxMemory is expected to be used
			expectedCPU:    resource.MustParse("5"),   // MaxCpu is expected to be used
			annotations: vpa_api_util.ContainerToAnnotationsMap{
				containerName: []string{"cpu capped to maxAllowed", "memory capped to maxAllowed"},
			},
		},
		{
			name:           "initialized pod",
			pod:            initialized,
			vpa:            vpa,
			expectedAction: true,
			expectedMem:    resource.MustParse("200Mi"),
			expectedCPU:    resource.MustParse("2"),
		},
		{
			name:           "high memory",
			pod:            initialized,
			vpa:            vpaWithHighMemory,
			expectedAction: true,
			expectedMem:    resource.MustParse("1000Mi"),
			expectedCPU:    resource.MustParse("2"),
		},
		{
			name:           "empty recommendation",
			pod:            initialized,
			vpa:            vpaWithEmptyRecommendation,
			expectedAction: true,
			expectedMem:    resource.MustParse("0"),
			expectedCPU:    resource.MustParse("0"),
		},
		{
			name:           "nil recommendation",
			pod:            initialized,
			vpa:            vpaWithNilRecommendation,
			expectedAction: true,
			expectedMem:    resource.MustParse("0"),
			expectedCPU:    resource.MustParse("0"),
		},
		{
			name:             "guaranteed resources",
			pod:              limitsMatchRequestsPod,
			vpa:              vpa,
			expectedAction:   true,
			expectedMem:      resource.MustParse("200Mi"),
			expectedCPU:      resource.MustParse("2"),
			expectedCPULimit: mustParseResourcePointer("2"),
			expectedMemLimit: mustParseResourcePointer("200Mi"),
		},
		{
			name:             "guaranteed resources - no request",
			pod:              limitsNoRequestsPod,
			vpa:              vpa,
			expectedAction:   true,
			expectedMem:      resource.MustParse("200Mi"),
			expectedCPU:      resource.MustParse("2"),
			expectedCPULimit: mustParseResourcePointer("2"),
			expectedMemLimit: mustParseResourcePointer("200Mi"),
		},
		{
			name:             "proportional limit - as default",
			pod:              podWithDoubleLimit,
			vpa:              vpa,
			expectedAction:   true,
			expectedCPU:      resource.MustParse("2"),
			expectedMem:      resource.MustParse("200Mi"),
			expectedCPULimit: mustParseResourcePointer("4"),
			expectedMemLimit: mustParseResourcePointer("400Mi"),
		},
		{
			name:             "proportional limit - set explicit",
			pod:              podWithDoubleLimit,
			vpa:              resourceRequestsAndLimitsVPA,
			expectedAction:   true,
			expectedCPU:      resource.MustParse("2"),
			expectedMem:      resource.MustParse("200Mi"),
			expectedCPULimit: mustParseResourcePointer("4"),
			expectedMemLimit: mustParseResourcePointer("400Mi"),
		},
		{
			name:           "disabled limit scaling",
			pod:            podWithDoubleLimit,
			vpa:            resourceRequestsOnlyVPA,
			expectedAction: true,
			expectedCPU:    resource.MustParse("2"),
			expectedMem:    resource.MustParse("200Mi"),
		},
		{
			name:           "disabled limit scaling - Requests capped at limit",
			pod:            podWithDoubleLimit,
			vpa:            resourceRequestsOnlyVPAHighTarget,
			expectedAction: true,
			expectedCPU:    resource.MustParse("2"),
			expectedMem:    resource.MustParse("200Mi"),
			annotations: vpa_api_util.ContainerToAnnotationsMap{
				containerName: []string{
					"cpu capped to container limit",
					"memory capped to container limit",
				},
			},
		},
		{
			name:             "limit over int64",
			pod:              podWithTenfoldLimit,
			vpa:              vpaWithExabyteRecommendation,
			expectedAction:   true,
			expectedCPU:      resource.MustParse("1Ei"),
			expectedMem:      resource.MustParse("1Ei"),
			expectedCPULimit: resource.NewMilliQuantity(math.MaxInt64, resource.DecimalExponent),
			expectedMemLimit: resource.NewQuantity(math.MaxInt64, resource.DecimalExponent),
			annotations: vpa_api_util.ContainerToAnnotationsMap{
				containerName: []string{
					"cpu: failed to keep limit to request ratio; capping limit to int64",
					"memory: failed to keep limit to request ratio; capping limit to int64",
				},
			},
		},
		{
			name:              "limit range calculation error",
			pod:               initialized,
			vpa:               vpa,
			limitRangeCalcErr: fmt.Errorf("oh no"),
			expectedAction:    false,
			expectedError:     fmt.Errorf("error getting containerLimitRange: oh no"),
		},
		{
			name:             "proportional limit from default",
			pod:              initialized,
			vpa:              vpa,
			expectedAction:   true,
			expectedCPU:      resource.MustParse("2"),
			expectedMem:      resource.MustParse("200Mi"),
			expectedCPULimit: mustParseResourcePointer("2"),
			expectedMemLimit: mustParseResourcePointer("200Mi"),
			limitRange: &apiv1.LimitRangeItem{
				Type: apiv1.LimitTypeContainer,
				Default: apiv1.ResourceList{
					apiv1.ResourceCPU:    resource.MustParse("2"),
					apiv1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			recommendationProvider := &recommendationProvider{
				recommendationProcessor: vpa_api_util.NewCappingRecommendationProcessor(limitrange.NewNoopLimitsCalculator()),
				limitsRangeCalculator: &fakeLimitRangeCalculator{
					containerLimitRange: tc.limitRange,
					containerErr:        tc.limitRangeCalcErr,
				},
			}

			resources, annotations, err := recommendationProvider.GetContainersResourcesForPod(tc.pod, tc.vpa)

			if tc.expectedAction {
				assert.Nil(t, err)
				if !assert.Equal(t, len(resources), 1) {
					return
				}

				cpuRequest := resources[0].Requests[apiv1.ResourceCPU]
				assert.Equal(t, tc.expectedCPU.Value(), cpuRequest.Value(), "cpu request doesn't match")

				memoryRequest := resources[0].Requests[apiv1.ResourceMemory]
				assert.Equal(t, tc.expectedMem.Value(), memoryRequest.Value(), "memory request doesn't match")

				cpuLimit, cpuLimitPresent := resources[0].Limits[apiv1.ResourceCPU]
				if tc.expectedCPULimit == nil {
					assert.False(t, cpuLimitPresent, "expected no cpu limit, got %s", cpuLimit.String())
				} else {
					if assert.True(t, cpuLimitPresent, "expected cpu limit, but it's missing") {
						assert.Equal(t, tc.expectedCPULimit.MilliValue(), cpuLimit.MilliValue(), "cpu limit doesn't match")
					}
				}

				memLimit, memLimitPresent := resources[0].Limits[apiv1.ResourceMemory]
				if tc.expectedMemLimit == nil {
					assert.False(t, memLimitPresent, "expected no memory limit, got %s", memLimit.String())
				} else {
					if assert.True(t, memLimitPresent, "expected memory limit, but it's missing") {
						assert.Equal(t, tc.expectedMemLimit.MilliValue(), memLimit.MilliValue(), "memory limit doesn't match")
					}
				}

				assert.Len(t, annotations, len(tc.annotations))
				if len(tc.annotations) > 0 {
					for annotationKey, annotationValues := range tc.annotations {
						assert.Len(t, annotations[annotationKey], len(annotationValues))
						for _, annotation := range annotationValues {
							assert.Contains(t, annotations[annotationKey], annotation)
						}
					}
				}
			} else {
				assert.Empty(t, resources)
				if tc.expectedError != nil {
					assert.Error(t, err)
					assert.Equal(t, tc.expectedError.Error(), err.Error())
				} else {
					assert.NoError(t, err)
				}
			}

		})

	}
}

func makeResourceList(millicores, mib int64) apiv1.ResourceList {
	cores := resource.NewMilliQuantity(millicores, resource.DecimalSI)
	mem := resource.NewQuantity(mib*1048576, resource.DecimalSI)
	return apiv1.ResourceList{apiv1.ResourceCPU: *cores, apiv1.ResourceMemory: *mem}
}

func Test_percentageToTarget(t *testing.T) {
	type args struct {
		requests   apiv1.ResourceList
		target     apiv1.ResourceList
		proportion float64
		inQoS      bool
	}
	tests := []struct {
		name string
		args args
		want apiv1.ResourceList
	}{
		{"Negative", args{makeResourceList(1000, 1024), makeResourceList(500, 512), .50, false},
			makeResourceList(750, 768)},
		{"Half", args{makeResourceList(500, 512), makeResourceList(1000, 1024), .50, false},
			makeResourceList(750, 768)},
		{"No Scaling", args{makeResourceList(500, 512), makeResourceList(1000, 1024), 0.0, false},
			makeResourceList(500, 512)},
		{"All the Way", args{makeResourceList(500, 512), makeResourceList(1000, 1024), 1.0, false},
			makeResourceList(1000, 1024)},
		{"Negative QoS Tight  - Early", args{makeResourceList(4200, 1024), makeResourceList(4000, 1024), 0.0, true},
			makeResourceList(4000, 1024)},
		{"Negative QoS Tight - Late", args{makeResourceList(4200, 1024), makeResourceList(4000, 1024), .95, true},
			makeResourceList(4000, 1024)},
		{"QoS Wide - Start", args{makeResourceList(500, 1024), makeResourceList(6000, 1024), .00, true},
			makeResourceList(1000, 1024)},
		{"QoS Wide - Early", args{makeResourceList(500, 1024), makeResourceList(6000, 1024), .01, true},
			makeResourceList(1000, 1024)},
		{"QoS Wide - Mid", args{makeResourceList(500, 1024), makeResourceList(6000, 1024), .65, true},
			makeResourceList(4000, 1024)},
		{"QoS Wide - Late", args{makeResourceList(500, 1024), makeResourceList(6000, 1024), .98, true},
			makeResourceList(6000, 1024)},
		{"QoS Wide - End", args{makeResourceList(500, 1024), makeResourceList(6000, 1024), 1.0, true},
			makeResourceList(6000, 1024)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, percentageToTarget(tt.args.requests, tt.args.target, tt.args.proportion), "percentageToTarget(%v, %v)", tt.args.requests, tt.args.target)
		})
	}
}

type containerSpec struct {
	Name string
	// cpu then RAM
	Requests []string
	Limits   []string
}

func cspec(name, cpuReq, memReq, cpuLim, memLim string) containerSpec {
	return containerSpec{name, []string{cpuReq, memReq}, []string{cpuLim, memLim}}
}

// cpuFix forces the Quantity's scale to -3.
func cpuFix(qty resource.Quantity) resource.Quantity {
	mills := qty.MilliValue()
	return *resource.NewMilliQuantity(mills, resource.DecimalSI)
}

func strip(qty resource.Quantity) resource.Quantity {
	// This invalidates the 's' property of the Quantity
	qty.Neg()
	qty.Neg()
	return qty
}

func res(cpu, mem string) core.ResourceList {
	return core.ResourceList{
		core.ResourceCPU:    strip(cpuFix(resource.MustParse(cpu))),
		core.ResourceMemory: strip(resource.MustParse(mem)),
	}
}

func podWithContainers(containers []containerSpec) *core.Pod {
	builtContainers := make([]core.Container, len(containers))
	for i, c := range containers {
		builtContainers[i] = core.Container{
			Name: c.Name,
			Resources: core.ResourceRequirements{
				Limits:   res(c.Limits[0], c.Limits[1]),
				Requests: res(c.Requests[0], c.Requests[1]),
			},
		}
	}

	return &core.Pod{
		Spec: core.PodSpec{Containers: builtContainers}}
}

func vpaMode(qos bool, coreRound int64, ramPerCore string) map[string]string {
	result := map[string]string{annotations.AnnotationQosEnable: strconv.FormatBool(qos),
		annotations.AnnotationCoreDivisor: strconv.FormatInt(coreRound, 10)}

	if len(ramPerCore) > 0 {
		qty := resource.MustParse(ramPerCore)
		result[annotations.AnnotationRamPerCore] = qty.String()
	}
	return result
}

func rec(nameCpuMem ...string) vpa_types.RecommendedPodResources {
	var result vpa_types.RecommendedPodResources

	for i := 0; i+2 < len(nameCpuMem); i += 3 {
		name := nameCpuMem[i]
		cpu := nameCpuMem[i+1]
		mem := nameCpuMem[i+2]
		result.ContainerRecommendations = append(result.ContainerRecommendations,
			vpa_types.RecommendedContainerResources{
				ContainerName: name,
				Target:        res(cpu, mem),
			})
	}

	return result
}

func resources(reqCpuMemLimCpuMem ...string) []vpa_api_util.ContainerResources {
	var result []vpa_api_util.ContainerResources

	for i := 0; i+3 < len(reqCpuMemLimCpuMem); i += 4 {
		reqCpu := reqCpuMemLimCpuMem[i+0]
		reqMem := reqCpuMemLimCpuMem[i+1]
		limCpu := reqCpuMemLimCpuMem[i+2]
		limMem := reqCpuMemLimCpuMem[i+3]
		result = append(result, vpa_api_util.ContainerResources{
			Requests: res(reqCpu, reqMem),
			Limits:   res(limCpu, limMem),
		})
	}
	return result
}

func TestGetContainersResources(t *testing.T) {
	type args struct {
		pod               *core.Pod
		vpaResourcePolicy *vpa_types.PodResourcePolicy
		podRecommendation vpa_types.RecommendedPodResources
		limitRange        *core.LimitRangeItem
		vpaAnnotations    map[string]string
		enableQoS         bool
	}
	emptyAnnotations := make(vpa_api_util.ContainerToAnnotationsMap)
	// Use the full target for recommendations.
	flag.Set("percentage", "100")
	tests := []struct {
		name string
		args args
		want []vpa_api_util.ContainerResources
	}{
		{name: "NonQoS", args: args{
			pod:               podWithContainers([]containerSpec{cspec("foo", "1", "100Mi", "2", "200Mi")}),
			podRecommendation: rec("foo", "1.5", "128Mi"),
			vpaAnnotations:    vpaMode(false, 1, ""),
		},
			want: resources("1.5", "128Mi", "3", "256Mi")},
		{name: "BasicQoS", args: args{
			pod:               podWithContainers([]containerSpec{cspec("foo", "1", "100Mi", "2", "200Mi")}),
			podRecommendation: rec("foo", "1.5", "128Mi"),
			vpaAnnotations:    vpaMode(true, 1, ""),
			enableQoS:         true,
		},
			want: resources("2", "128Mi", "2", "128Mi")},
		{name: "MoreCoresQoS", args: args{
			pod:               podWithContainers([]containerSpec{cspec("foo", "1", "100Mi", "2", "200Mi")}),
			podRecommendation: rec("foo", "1.5", "128Mi"),
			vpaAnnotations:    vpaMode(true, 4, ""),
			enableQoS:         true,
		},
			want: resources("4", "128Mi", "4", "128Mi")},
		{name: "RamPerCoreQoS", args: args{
			pod:               podWithContainers([]containerSpec{cspec("foo", "1", "100Mi", "2", "200Mi")}),
			podRecommendation: rec("foo", "1.5", "128Mi"),
			vpaAnnotations:    vpaMode(true, 1, "32Mi"),
			enableQoS:         true,
		},
			want: resources("4", "128Mi", "4", "128Mi")},
		{name: "RamPerManyCoresQoS", args: args{
			pod:               podWithContainers([]containerSpec{cspec("foo", "1", "100Mi", "2", "200Mi")}),
			podRecommendation: rec("foo", "1.5", "128Mi"),
			vpaAnnotations:    vpaMode(true, 4, "64Mi"),
			enableQoS:         true,
		},
			want: resources("4", "256Mi", "4", "256Mi")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, GetContainersResources(tt.args.pod, tt.args.vpaResourcePolicy, tt.args.podRecommendation, tt.args.limitRange, true, emptyAnnotations, tt.args.vpaAnnotations, tt.args.enableQoS), "GetContainersResources(%v, %v, %v, %v, false, [],  %v, %v)", tt.args.pod, tt.args.vpaResourcePolicy, tt.args.podRecommendation, tt.args.limitRange, tt.args.vpaAnnotations, tt.args.enableQoS)
		})
	}
}

func Test_percentageToTarget1(t *testing.T) {
	type args struct {
		requests   apiv1.ResourceList
		target     apiv1.ResourceList
		proportion float64
	}
	tests := []struct {
		name string
		args args
		want apiv1.ResourceList
	}{
		{"zero", args{res("0.5", "128Mi"), res("1", "128Mi"), 0}, res("0.5", "128Mi")},
		{"half", args{res("0.5", "1Gi"), res("1", "3Gi"), 0.5}, res("0.75", "2Gi")},
		{"full", args{res("0.5", "1Gi"), res("1", "3Gi"), 1}, res("1", "3Gi")},
		{"neg-half", args{res("1", "3Gi"), res("0.5", "1Gi"), 0.5}, res("0.75", "2Gi")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, percentageToTarget(tt.args.requests, tt.args.target, tt.args.proportion), "percentageToTarget(%v, %v, %v)", tt.args.requests, tt.args.target, tt.args.proportion)
		})
	}
}
