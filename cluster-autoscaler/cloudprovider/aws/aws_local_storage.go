package aws

import (
	"strings"
)

var volumesPerInstanceFamilly = map[string]int64{
	"c1":   1,
	"c3":   1,
	"c5ad": 1,
	"c5d":  1,
	"c6gd": 1,
	"d2":   1,
	"f1":   1,
	"g2":   1,
	"g4dn": 1,
	"h1":   1,
	"i2":   1,
	"i3":   1,
	"i3en": 1,
	"i3p":  1,
	"m1":   1,
	"m2":   1,
	"m3":   1,
	"m5ad": 1,
	"m5d":  1,
	"m5dn": 1,
	"m6gd": 1,
	"p3dn": 1,
	"r3":   1,
	"r5ad": 1,
	"r5d":  1,
	"r5dn": 1,
	"r6gd": 1,
	"x1":   1,
	"x1e":  1,
	"z1d":  1,
}

func numberOfLocalVolumes(instanceType string) int64 {
	familly := strings.Split(instanceType, ".")[0]
	if volumes, ok := volumesPerInstanceFamilly[familly]; ok {
		return volumes
	}
	return 0
}
