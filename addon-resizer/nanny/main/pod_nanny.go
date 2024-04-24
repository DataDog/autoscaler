/*
Copyright 2016 The Kubernetes Authors.

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

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	flag "github.com/spf13/pflag"

	"k8s.io/autoscaler/addon-resizer/healthcheck"
	"k8s.io/autoscaler/addon-resizer/nanny"

	"path/filepath"

	"github.com/golang/glog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/autoscaler/addon-resizer/nanny/apis/nannyconfig"
	nannyscheme "k8s.io/autoscaler/addon-resizer/nanny/apis/nannyconfig/scheme"
	nannyconfigalpha "k8s.io/autoscaler/addon-resizer/nanny/apis/nannyconfig/v1alpha1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	// Flags to define the resource requirements.
	configDir = flag.String("config-dir", nannyconfig.NoValue, "Path of configuration containing base resource requirements.")
	// Following empty values ("") will be overwritten by defaults specified in apis/nannyconfig/v1alpha1/defaults.go
	baseCPU            = flag.String("cpu", "", "The base CPU resource requirement.")
	cpuPerResource     = flag.String("extra-cpu", "", "The amount of CPU to add per resource.")
	baseMemory         = flag.String("memory", "", "The base memory resource requirement.")
	memoryPerResource  = flag.String("extra-memory", "", "The amount of memory to add per resource.")
	baseStorage        = flag.String("storage", nannyconfig.NoValue, "The base storage resource requirement.")
	storagePerResource = flag.String("extra-storage", "0Gi", "The amount of storage to add per resource.")
	scaleDownDelay     = flag.Duration("scale-down-delay", time.Duration(0), "The time to wait after the addon-resizer start or last scaling operation before the scale down can be performed.")
	scaleUpDelay       = flag.Duration("scale-up-delay", time.Duration(0), "The time to wait after the addon-resizer start or last scaling operation before the scale up can be performed.")
	threshold          = flag.Int("threshold", 0, "A number between 0-100. The dependent's resources are rewritten when they deviate from expected by more than threshold.")
	// Flags to identify the container to nanny.
	podNamespace  = flag.String("namespace", os.Getenv("MY_POD_NAMESPACE"), "The namespace of the ward. This defaults to the nanny pod's own namespace.")
	deployment    = flag.String("deployment", "", "The name of the deployment being monitored. This is required.")
	podName       = flag.String("pod", os.Getenv("MY_POD_NAME"), "The name of the pod to watch. This defaults to the nanny's own pod.")
	containerName = flag.String("container", "pod-nanny", "The name of the container to watch. This defaults to the nanny itself.")
	// Flags to control runtime behavior.
	pollPeriod     = flag.Int("poll-period", 10000, "The time, in milliseconds, to poll the dependent container.")
	estimator      = flag.String("estimator", "linear", "The estimator to use. Currently supported: linear, exponential")
	minClusterSize = flag.Uint64("minClusterSize", 16, "The smallest number of resources will be scaled to. Must be > 1. This flag is used only when an exponential estimator is used.")
	useMetrics     = flag.Bool("use-metrics", false, "Whether to use apiserver metrics to detect cluster size instead of the default method of listing objects from the Kubernetes API.")
	hcAddress      = flag.String("healthcheck-address", ":8080", "The address to expose an HTTP health-check on.")
	scalingMode    = flag.String("scaling-mode", nanny.NodeProportional, "The mode of scaling to be used. Possible values: 'node-proportional' or 'container-proportional'")
	// Flags for addon resizer running on GKE masters.
	runOnMaster                 = flag.Bool("run-on-master", false, "Whether the addon-resizer is running on GKE master VMs.")
	kubeconfigPath              = flag.String("kubeconfig", "", "absolute path to the kubeconfig file specifying the apiserver instance.")
	nannyConfigName             = flag.String("nanny-config-name", "", "The name of the ConfigMap of NannyConfiguration.")
    leaderElectionEnable        = flag.Bool("leader_election_enable", false, "Whether leader election should be used.")
    leaderElectionLeaseDuration = flag.Duration("leader_election_lease_duration", 15*time.Second, "The duration that non-leader candidates will wait to force acquire leadership.")
	leaderElectionRenewDeadline = flag.Duration("leader_election_renew_deadline", 10*time.Second, "The duration that the acting master will retry refreshing leadership before giving up.")
	leaderElectionRetryPeriod   = flag.Duration("leader_election_retry_period", 2*time.Second, "The duration to wait between tries of actions.")
)

func main() {
	// First log our starting config, and then set up.
	glog.Infof("Invoked by %v", os.Args)
	glog.Infof("Version: %s", nanny.AddonResizerVersion)
	flag.Parse()

	// Perform further validation of flags.
	if *deployment == "" {
		glog.Fatal("Must specify a deployment.")
	}

	if *threshold < 0 || *threshold > 100 {
		glog.Fatalf("Threshold must be between 0 and 100 inclusive. It is %d.", *threshold)
	}

	if *minClusterSize < 2 {
		glog.Fatalf("minClusterSize must be greater than 1. It is set to %d.", *minClusterSize)
	}

	glog.Infof("Watching namespace: %s, pod: %s, container: %s.", *podNamespace, *podName, *containerName)
	glog.Infof("storage: %s, extra_storage: %s", *baseStorage, *storagePerResource)

	// Set up work objects.
	config, err := rest.InClusterConfig()
	if err != nil {
		if !*runOnMaster {
			glog.Fatal(err)
		}
		// When running on GKE master, config is setup in a different way.
		if *kubeconfigPath == "" {
			config = &rest.Config{Host: "http://localhost:8080"}
		} else {
			config, err = clientcmd.BuildConfigFromFlags("", *kubeconfigPath)
			if err != nil {
				glog.Exitf("Failed to load k8s client config: %v", err)
			}
		}
	}
	config.UserAgent = userAgent()
	// Use protobufs for communication with apiserver
	config.ContentType = "application/vnd.kubernetes.protobuf"

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatal(err)
	}
	// Use protobufs to improve performance.
	config.ContentType = "application/vnd.kubernetes.protobuf"

	k8s := nanny.NewKubernetesClient(*podNamespace, *deployment, *podName, *containerName, clientset, *useMetrics, *runOnMaster)

	nannyConfigurationFromFlags := &nannyconfigalpha.NannyConfiguration{
		BaseCPU:       *baseCPU,
		CPUPerNode:    *cpuPerResource,
		BaseMemory:    *baseMemory,
		MemoryPerNode: *memoryPerResource,
	}

	var resources []nanny.Resource
	var nannyConfigUpdator nanny.NannyConfigUpdator

	if *runOnMaster {
		nannyConfigUpdator = newNannyConfigUpdator(clientset, nannyConfigurationFromFlags, *podNamespace, *nannyConfigName, *baseStorage)
	} else {
        nannycfg, err := loadNannyConfiguration(*configDir, nannyConfigurationFromFlags)
		if err != nil {
			glog.Fatal(err)
		}
		glog.Infof("cpu: %s, extra_cpu: %s, memory: %s, extra_memory: %s", nannycfg.BaseCPU, nannycfg.CPUPerNode, nannycfg.BaseMemory, nannycfg.MemoryPerNode)
		resources = updateResources(nannycfg, *baseStorage)
	}

	var est nanny.ResourceEstimator
	if *estimator == "linear" {
		est = nanny.LinearEstimator{
			Resources: resources,
		}
	} else if *estimator == "exponential" {
		est = nanny.ExponentialEstimator{
			Resources:      resources,
			ScaleFactor:    1.5,
			MinClusterSize: *minClusterSize,
		}
	} else {
		glog.Fatalf("Estimator %s not supported", *estimator)
	}

	if *scalingMode != nanny.NodeProportional && *scalingMode != nanny.ContainerProportional {
		glog.Fatalf("scaling mode %s not supported", *scalingMode)
	}

	period := time.Duration(*pollPeriod) * time.Millisecond
	hc := healthcheck.NewHealthCheck(*hcAddress, period*5)
	hc.Serve()

	// Begin nannying.
	start := func() {
		nanny.PollAPIServer(k8s, est, hc, period, *scaleDownDelay, *scaleUpDelay, uint64(*threshold), *scalingMode, *runOnMaster, nannyConfigUpdator)
	}

	if !*runOnMaster || !*leaderElectionEnable {
		start()
	} else {
		glog.Info("Leader Election Enabled.")
		nanny.LeadOrDie(
			nanny.Config{
				LeaseDuration:   *leaderElectionLeaseDuration,
				RenewDeadline:   *leaderElectionRenewDeadline,
				RetryPeriod:     *leaderElectionRetryPeriod,
				SystemNamespace: *podNamespace,
			},
			clientset, start)
	}
}

func userAgent() string {
	command := ""
	if len(os.Args) > 0 && len(os.Args[0]) > 0 {
		command = filepath.Base(os.Args[0])
	}
	if len(command) == 0 {
		command = "addon-resizer"
	}
	return command + "/" + nanny.AddonResizerVersion
}

func loadNannyConfiguration(configDir string, defaultConfig *nannyconfigalpha.NannyConfiguration) (*nannyconfig.NannyConfiguration, error) {
	path := filepath.Join(configDir, "NannyConfiguration")
	_, codecs, err := nannyscheme.NewSchemeAndCodecs()
	if err != nil {
		return nil, err
	}
	// overwrite defaults with flag-specified parameters
	nannyconfigalpha.SetDefaults_NannyConfiguration(defaultConfig)
	// retrieve config map parameters if present
	configMapConfig := &nannyconfigalpha.NannyConfiguration{}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		glog.V(0).Infof("Failed to read data from config file %q: %v, using default parameters", path, err)
	} else if configMapConfig, err = decodeNannyConfiguration(data, codecs); err != nil {
		configMapConfig = &nannyconfigalpha.NannyConfiguration{}
		glog.V(0).Infof("Unable to decode Nanny Configuration from config map, using default parameters")
	}
	nannyconfigalpha.SetDefaults_NannyConfiguration(configMapConfig)
	// overwrite defaults with config map parameters
	nannyconfigalpha.FillInDefaults_NannyConfiguration(configMapConfig, defaultConfig)
	return convertNannyConfiguration(configMapConfig), nil
}

func convertNannyConfiguration(configAlpha *nannyconfigalpha.NannyConfiguration) *nannyconfig.NannyConfiguration {
	if configAlpha == nil {
		return nil
	}
	return &nannyconfig.NannyConfiguration{
		TypeMeta:      configAlpha.TypeMeta,
		BaseCPU:       configAlpha.BaseCPU,
		CPUPerNode:    configAlpha.CPUPerNode,
		BaseMemory:    configAlpha.BaseMemory,
		MemoryPerNode: configAlpha.MemoryPerNode,
	}
}

func decodeNannyConfiguration(data []byte, codecs *serializer.CodecFactory) (*nannyconfigalpha.NannyConfiguration, error) {
	obj, err := runtime.Decode(codecs.UniversalDecoder(nannyconfigalpha.SchemeGroupVersion), data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode, error: %v", err)
	}
	externalHC, ok := obj.(*nannyconfigalpha.NannyConfiguration)
	if !ok {
		return nil, fmt.Errorf("failed to cast object to NannyConfiguration, object: %#v", obj)
	}
	return externalHC, nil
}

func updateResources(nannycfg *nannyconfig.NannyConfiguration, baseStorage string) []nanny.Resource {
	var resources []nanny.Resource

	// Monitor only the resources specified.
	if nannycfg.BaseCPU != nannyconfig.NoValue {
		resources = append(resources, nanny.Resource{
			Base:         resource.MustParse(nannycfg.BaseCPU),
			ExtraPerNode: resource.MustParse(nannycfg.CPUPerNode),
			Name:         "cpu",
		})
	}

	if nannycfg.BaseMemory != nannyconfig.NoValue {
		resources = append(resources, nanny.Resource{
			Base:         resource.MustParse(nannycfg.BaseMemory),
			ExtraPerNode: resource.MustParse(nannycfg.MemoryPerNode),
			Name:         "memory",
		})
	}

	if baseStorage != nannyconfig.NoValue {
		resources = append(resources, nanny.Resource{
			Base:         resource.MustParse(baseStorage),
			ExtraPerNode: resource.MustParse(nannycfg.MemoryPerNode),
			Name:         "storage",
		})
	}

	glog.V(4).Infof("Resources: %+v", resources)
	return resources
}

type nannyConfigUpdator struct {
	clientset       *kubernetes.Clientset
	defaultConfig   *nannyconfigalpha.NannyConfiguration
	namespace       string
	nannyConfigName string
	baseStorage     string
}

// newNannyConfigUpdator gives a NannyConfigUpdator 
// with the given dependencies.
func newNannyConfigUpdator(clientset *kubernetes.Clientset, defaultConfig *nannyconfigalpha.NannyConfiguration, namespace, nannyConfigName, baseStorage string) nanny.NannyConfigUpdator {
	result := &nannyConfigUpdator{
		clientset:       clientset,
		defaultConfig:   defaultConfig,
		namespace:       namespace,
		nannyConfigName: nannyConfigName,
		baseStorage:     baseStorage,
	}
	return result
}

// CurrentResources fetches latest data from NannyConfiguration
// through API calls and returns required resources.
func (n *nannyConfigUpdator) CurrentResources() ([]nanny.Resource, error) {
	_, codecs, err := nannyscheme.NewSchemeAndCodecs()
	if err != nil {
		return nil, err
	}
	// overwrite defaults with flag-specified parameters
	nannyconfigalpha.SetDefaults_NannyConfiguration(n.defaultConfig)
	// retrieve config map parameters if present
	configMapConfig := &nannyconfigalpha.NannyConfiguration{}

	nannycfg, err := n.clientset.CoreV1().ConfigMaps(n.namespace).Get(context.Background(), n.nannyConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	data := []byte(nannycfg.Data["NannyConfiguration"])
	configMapConfig, err = decodeNannyConfiguration(data, codecs)
	if err != nil {
		configMapConfig = &nannyconfigalpha.NannyConfiguration{}
		glog.V(0).Infof("Unable to decode Nanny Configuration from config map, using default parameters: %v", err)
	}

    nannyconfigalpha.SetDefaults_NannyConfiguration(configMapConfig)
	// overwrite defaults with config map parameters
	nannyconfigalpha.FillInDefaults_NannyConfiguration(configMapConfig, n.defaultConfig)
	return updateResources(convertNannyConfiguration(configMapConfig), n.baseStorage), nil
}