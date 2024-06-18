/*
Copyright 2021 The Kubernetes Authors.

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

package grpcplugin

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/expander/grpcplugin/protos"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/test"
	"k8s.io/autoscaler/cluster-autoscaler/expander"

	_ "github.com/golang/mock/mockgen/model"
)

var (
	eoT2MicroWithSimilar = expander.Option{
		Debug:             "t2.micro",
		NodeGroup:         test.NewTestNodeGroup("my-asg.t2.micro", 10, 1, 1, true, false, "t2.micro", nil, nil),
		SimilarNodeGroups: []cloudprovider.NodeGroup{test.NewTestNodeGroup("my-similar-asg.t2.micro", 10, 1, 1, true, false, "t2.micro", nil, nil)},
	}

	grpcEoT2MicroWithSimilar = protos.Option{
		NodeGroupId:         eoT2Micro.NodeGroup.Id(),
		NodeCount:           int32(eoT2Micro.NodeCount),
		Debug:               eoT2Micro.Debug,
		Pod:                 eoT2Micro.Pods,
		SimilarNodeGroupIds: []string{eoT2MicroWithSimilar.SimilarNodeGroups[0].Id()},
	}
	grpcEoT2MicroWithSimilarWithExtraOptions = protos.Option{
		NodeGroupId:         eoT2Micro.NodeGroup.Id(),
		NodeCount:           int32(eoT2Micro.NodeCount),
		Debug:               eoT2Micro.Debug,
		Pod:                 eoT2Micro.Pods,
		SimilarNodeGroupIds: []string{eoT2MicroWithSimilar.SimilarNodeGroups[0].Id(), "extra-ng-id"},
	}
)

func TestPopulateOptionsForGrpcSimilarNodegroupsAreIncluded(t *testing.T) {
	testCases := []struct {
		desc         string
		opts         []expander.Option
		expectedOpts []*protos.Option
		expectedMap  map[string]expander.Option
	}{
		{
			desc:         "similar nodegroups are included",
			opts:         []expander.Option{eoT2MicroWithSimilar},
			expectedOpts: []*protos.Option{&grpcEoT2MicroWithSimilar},
			expectedMap:  map[string]expander.Option{eoT2MicroWithSimilar.NodeGroup.Id(): eoT2MicroWithSimilar},
		},
	}
	for _, tc := range testCases {
		grpcOptionsSlice, nodeGroupIDOptionMap := populateOptionsForGRPC(tc.opts)
		assert.Equal(t, tc.expectedOpts, grpcOptionsSlice)
		assert.Equal(t, tc.expectedMap, nodeGroupIDOptionMap)
	}
}

func TestValidTransformAndSanitizeOptionsFromGRPCWithSimilar(t *testing.T) {
	testCases := []struct {
		desc                  string
		responseOptions       []*protos.Option
		expectedOptions       []expander.Option
		nodegroupIDOptionaMap map[string]expander.Option
	}{
		{
			desc:            "valid transform and sanitize options",
			responseOptions: []*protos.Option{&grpcEoT2Micro, &grpcEoT3Large, &grpcEoM44XLarge},
			nodegroupIDOptionaMap: map[string]expander.Option{
				eoT2Micro.NodeGroup.Id():   eoT2Micro,
				eoT2Large.NodeGroup.Id():   eoT2Large,
				eoT3Large.NodeGroup.Id():   eoT3Large,
				eoM44XLarge.NodeGroup.Id(): eoM44XLarge,
			},
			expectedOptions: []expander.Option{eoT2Micro, eoT3Large, eoM44XLarge},
		},
		{
			desc:            "similar ngs are retained in proto options are retained",
			responseOptions: []*protos.Option{&grpcEoT2MicroWithSimilar},
			nodegroupIDOptionaMap: map[string]expander.Option{
				eoT2MicroWithSimilar.NodeGroup.Id(): eoT2MicroWithSimilar,
			},
			expectedOptions: []expander.Option{eoT2MicroWithSimilar},
		},
		{
			desc:            "extra similar ngs added to expander response are ignored",
			responseOptions: []*protos.Option{&grpcEoT2MicroWithSimilarWithExtraOptions},
			nodegroupIDOptionaMap: map[string]expander.Option{
				eoT2MicroWithSimilar.NodeGroup.Id(): eoT2MicroWithSimilar,
			},
			expectedOptions: []expander.Option{eoT2MicroWithSimilar},
		},
	}
	for _, tc := range testCases {
		ret := transformAndSanitizeOptionsFromGRPC(tc.responseOptions, tc.nodegroupIDOptionaMap)
		assert.Equal(t, tc.expectedOptions, ret)
	}
}
