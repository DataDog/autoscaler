package azure

import (
	"regexp"
)

// https://github.com/DataDog/k8s-nodegroups/blob/controller-runtime-v1/pkg/cloud/azure/clients/resource_skus_cache.go#L21
var volumesPerInstanceFamilly = map[string]int64{
	"Standard_L\\d+s_v2": 1, // standardLSv2Family
	"Standard_E\\d+d_v4": 1, // standardEDv4Family
	"Standard_E.*ds_v4":  1, // standardEDSv4Family
}

func numberOfLocalVolumes(instanceType string) int64 {
	for familly, volumes := range volumesPerInstanceFamilly {
		if match, _ := regexp.MatchString(familly, instanceType); match {
			return volumes
		}
	}
	return 0
}
