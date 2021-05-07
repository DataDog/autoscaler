/*
  This serves two purposes:
  * Removes volumes using "local-data" from the internal copy of pods (for the duration
    of the current autoscaler RunOnce loop). local-data volumes (or any volume using a
    no-provisioner storage class) are breaking Scheduler Framework's evaluations.
  * Injects a custom resources request/limit to pods having "local-data".
    Our autoscaler fork is placing the same resource on NodeInfo templates built
    from ASG/MIG/VMSS using instance types that offer local-data storage.
    This restricts candidate/upscalable nodes that satisfy pods using local-data volumes.
*/
package pods

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/cluster-autoscaler/context"

	apiv1 "k8s.io/api/core/v1"
)

type transformLocalData struct{}

func NewTransformLocalData() *transformLocalData {
	return &transformLocalData{}
}

func (p *transformLocalData) CleanUp() {}

func (p *transformLocalData) Process(ctx *context.AutoscalingContext, pods []*apiv1.Pod) ([]*apiv1.Pod, error) {
	pvcLister := ctx.ListerRegistry.PersistentVolumeClaimLister()
	for _, po := range pods {
		var volumes []apiv1.Volume
		for _, vol := range po.Spec.Volumes {
			if vol.PersistentVolumeClaim == nil {
				volumes = append(volumes, vol)
				continue
			}
			pvc, err := pvcLister.PersistentVolumeClaims(po.Namespace).Get(vol.PersistentVolumeClaim.ClaimName)
			if err != nil {
				volumes = append(volumes, vol)
				continue
			}
			if *pvc.Spec.StorageClassName != "local-data" {
				volumes = append(volumes, vol)
				continue
			}

			if len(po.Spec.Containers[0].Resources.Requests) == 0 {
				po.Spec.Containers[0].Resources.Requests = apiv1.ResourceList{}
			}
			if len(po.Spec.Containers[0].Resources.Limits) == 0 {
				po.Spec.Containers[0].Resources.Limits = apiv1.ResourceList{}
			}

			po.Spec.Containers[0].Resources.Requests["storageclass/local-data"] = *resource.NewQuantity(1, resource.DecimalSI)
			po.Spec.Containers[0].Resources.Limits["storageclass/local-data"] = *resource.NewQuantity(1, resource.DecimalSI)
		}
		po.Spec.Volumes = volumes
	}

	return pods, nil
}
