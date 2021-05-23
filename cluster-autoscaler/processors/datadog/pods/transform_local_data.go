/*
  This hack brings support for local-data persistent volumes, in two ways:
  * Removes volumes using "local-data" from the internal copy of pods (for the
    duration of the current autoscaler RunOnce loop). local-data volumes (or any
    volume using a no-provisioner storage class) are breaking the VolumeBinding
    predicate used during Scheduler Framework's evaluations.
  * Injects a custom resources request to pods having "local-data" volumes.
    Our autoscaler fork is placing the same resource on NodeInfo templates
    built from ASG/MIG/VMSS using instance types that offer local-data storage.
    Those virtual NodeInfo are used when the autoscaler evaluates upscale
    candidates. Injecting those requests on pods allows us to upscale only
    nodes having local data, and to consider they can host only one such pod.

  Caveats:
  * With that resource, none of the existing real nodes can be considered by
    autoscaler as schedulable for pods requesting local-data volumes:
    the "storageclass/local-data" resource is only available on virtual nodes
    built from asg templates (so, during upscale simulations and for upcoming
    node, but not at "filter out pods schedulable on existing nodes" phase).
    Hence the other ("node is not ready while local-volume is not here") patch:
    it prevents spurious re-upscales when a pending pod (requesting local-data)
    is not yet scheduled on a node that just joined, as in that situation the
    autoscaler isn't able to filter out that pod as "schedulable".
  * Also, using that hack forces us to use nodeinfos built from asg templates,
    rather than from real world nodes (as op. to upstream behaviour).
  * That's obviously not upstreamable
*/
package pods

import (
	"time"

	"k8s.io/autoscaler/cluster-autoscaler/context"

	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/fields"
	client "k8s.io/client-go/kubernetes"
	v1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	klog "k8s.io/klog/v2"
)

type transformLocalData struct {
	pvcLister   v1lister.PersistentVolumeClaimLister
	stopChannel chan struct{}
}

func NewTransformLocalData() *transformLocalData {
	return &transformLocalData{
		stopChannel: make(chan struct{}),
	}
}

func (p *transformLocalData) CleanUp() {
	close(p.stopChannel)
}

func (p *transformLocalData) Process(ctx *context.AutoscalingContext, pods []*apiv1.Pod) ([]*apiv1.Pod, error) {
	// context.AutoscalingContext is not available at processors instanciation
	// time, hence a just-in-time registration on first Process call.
	if p.pvcLister == nil {
		p.pvcLister = NewPersistentVolumeClaimLister(ctx.ClientSet, p.stopChannel)
	}

	for _, po := range pods {
		var volumes []apiv1.Volume
		for _, vol := range po.Spec.Volumes {
			if vol.PersistentVolumeClaim == nil {
				volumes = append(volumes, vol)
				continue
			}
			pvc, err := p.pvcLister.PersistentVolumeClaims(po.Namespace).Get(vol.PersistentVolumeClaim.ClaimName)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					klog.Warningf("failed to fetch pvc for %s/%s: %v", po.GetNamespace(), po.GetName(), err)
				}
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

// NewPersistentVolumeClaimLister builds a persistentvolumeclaim lister.
func NewPersistentVolumeClaimLister(kubeClient client.Interface, stopchannel <-chan struct{}) v1lister.PersistentVolumeClaimLister {
	listWatcher := cache.NewListWatchFromClient(kubeClient.CoreV1().RESTClient(), "persistentvolumeclaims", apiv1.NamespaceAll, fields.Everything())
	store, reflector := cache.NewNamespaceKeyedIndexerAndReflector(listWatcher, &apiv1.PersistentVolumeClaim{}, time.Hour)
	lister := v1lister.NewPersistentVolumeClaimLister(store)
	go reflector.Run(stopchannel)
	return lister
}
