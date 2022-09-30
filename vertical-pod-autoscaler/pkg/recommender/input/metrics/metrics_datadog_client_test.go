package metrics

import (
	_context "context"
	"encoding/json"
	"fmt"
	"github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	assert "github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var fakeConfigFile string

func TestMain(m *testing.M) {
	// Create a temp file with fake api/app keys and pass its filename around.
	ofile, err := os.CreateTemp("/tmp", "vpa-test-*.json")
	if ofile == nil {
		fmt.Printf("Failed to create necessary temp file: %v, exiting.", err)
		os.Exit(1)
	}
	defer os.Remove(ofile.Name())
	configValues := make(map[string]string)
	configValues["apiKeyAuth"] = "00000000000000000000000000000000"
	configValues["appKeyAuth"] = "0000000000000000000000000000000000000000"
	bytes, err := json.Marshal(configValues)
	if err != nil {
		fmt.Printf("Failed to create temp config file: %v", err)
		os.Exit(1)
	}
	fakeConfigFile = ofile.Name()
	ofile.Write(bytes)
	os.Exit(m.Run())
}

func TestNewDatadogClient(t *testing.T) {
	if NewDatadogClient(time.Second, "test", fakeConfigFile, nil) == nil {
		t.Errorf("Client should have been created with a valid interval and cluster")
	}
	if NewDatadogClient(time.Microsecond, "test", fakeConfigFile, nil) != nil {
		t.Errorf("Client shouldn't have been created with a smaller query interval than Datadog reports")
	}
	if NewDatadogClient(time.Second, "", fakeConfigFile, nil) != nil {
		t.Errorf("Client shouldn't have been created without a cluster specified")
	}
}

func TestDdclientMetrics_PodMetricses(t *testing.T) {
	ddclient := NewDatadogClient(time.Second, "test", fakeConfigFile, nil)
	podMetrics := ddclient.PodMetricses("foo")
	if podMetrics == nil {
		t.Errorf("A valid PodMetricses call failed.")
	}
}

type testClient struct {
	baseClient
	retSequence        []datadog.MetricsQueryResponse
	memIndex           map[string]int
	cpuIndex           map[string]int
	requiredSubstrings []string
}

// Add metric data starting at time='start' and using randomized values for CPU (in cores) and mem (in MiB).
// Time start is in seconds since the epoch.
func (tc *testClient) addMetrics(key string, start int64, meanCpu float64, meanMem float64, nrSamples int64, tags map[string]string) error {
	cpuTemplate := []byte(
		`{"aggr": null, "attributes": {},
         "display_name": "kubernetes.cpu.usage.total", 
         "end": 0,
         "expression": "(removed)",
         "interval": 2,
         "length":0,
         "metric": "kubernetes.cpu.usage.total",
		 "pointlist": [],
		 "query_index": 0,
		 "scope": "(removed)",
		 "start": 0,
		 "tag_set": [],
		 "unit": [{"family": "cpu",
				   "id": 121,
				   "name": "nanocore",
				   "plural": "nanocores",
				   "scale_factor": 1e-09,
				   "short_name": "ncores"},
				  null]}`)

	memTemplate := []byte(
		`{"aggr": null,
	     "attributes": {},
		 "display_name": "kubernetes.memory.usage",
		 "end": 0,
		 "expression": "(removed)",
		 "interval": 2,
		 "length": 0,
		 "metric": "kubernetes.memory.usage",
		 "pointlist": [],
	     "query_index": 0,
		 "scope": "(removed))",
		 "start": 0,
		 "tag_set": [],
		 "unit": [{"family": "bytes",
				   "id": 2,
				   "name": "byte",
				   "plural": "bytes",
				   "scale_factor": 1.0,
				   "short_name": "B"},
				  null]}`)

	var blankCpu datadog.MetricsQueryMetadata
	var blankMem datadog.MetricsQueryMetadata

	err := json.Unmarshal(cpuTemplate, &blankCpu)
	if err != nil {
		return err
	}
	err = json.Unmarshal(memTemplate, &blankMem)
	if err != nil {
		return err
	}

	memIndex, err := tc.addMetricsElement(start, meanMem, 1e6, nrSamples, blankMem, tags)
	if err != nil {
		return err
	}
	cpuIndex, err := tc.addMetricsElement(start, meanCpu, 1e9, nrSamples, blankCpu, tags)
	if err != nil {
		return err
	}
	if tc.memIndex == nil {
		tc.memIndex = make(map[string]int)
	}
	if tc.cpuIndex == nil {
		tc.cpuIndex = make(map[string]int)
	}
	tc.memIndex[key] = memIndex
	tc.cpuIndex[key] = cpuIndex
	return nil
}

// A random value within 10% of the mean.  Useless for modeling, nice for decent bounds for testing against.
func boundedRandom(mean float64) float64 {
	stddev := mean * 0.1
	raw := rand.NormFloat64()*stddev + mean
	if raw < (0.9 * mean) {
		return 0.9 * mean
	} else if raw > (1.1 * mean) {
		return 1.1 * mean
	} else {
		return raw
	}
}

func (tc *testClient) addMetricsElement(start int64, mean float64, scale float64, nrSamples int64, blank datadog.MetricsQueryMetadata, tags map[string]string) (int, error) {
	resultTemplate := []byte(`{"from_date": 0,
	 "group_by": ["kube_cluster_name", "pod_name"],
	 "message": "",
	 "query": "(removed)",
	 "res_type": "time_series",
	 "resp_version": 1,
	 "series": [],
	 "status": "ok",
	 "times": [],
	 "to_date": 0,
	 "values": []}`)
	tagSet := make([]string, 0, len(tags))
	for k, v := range tags {
		tagSet = append(tagSet, fmt.Sprintf("%s:%s", k, v))
	}

	var result datadog.MetricsQueryResponse

	blank.TagSet = tagSet
	// Generate values for each series, then embed them into the result object for return.

	pointList := make([][]*float64, 0, nrSamples)
	for i := int64(0); i < nrSamples; i++ {
		t := new(float64)
		m := new(float64)
		// The destination units are specified in the template JSON vars above.
		*t = float64(start+i) * 1000.0
		*m = boundedRandom(mean) * scale
		pointList = append(pointList, []*float64{t, m})
	}
	blank.Pointlist = pointList
	err := json.Unmarshal(resultTemplate, &result)
	if err != nil {
		return -1, err
	}
	if tc.retSequence == nil {
		tc.retSequence = make([]datadog.MetricsQueryResponse, 0, 1)
	}
	resultSeries := make([]datadog.MetricsQueryMetadata, 0, 1)
	resultSeries = append(resultSeries, blank)
	result.Series = resultSeries
	result.FromDate = new(int64)
	*result.FromDate = start * 1000 // Dates are in milliseconds
	result.ToDate = new(int64)
	*result.ToDate = (start + nrSamples) * 1000
	retIndex := len(tc.retSequence)
	tc.retSequence = append(tc.retSequence, result)
	return retIndex, nil
}

func (tc testClient) QueryMetrics(context _context.Context, interval time.Duration, query string) (datadog.MetricsQueryResponse, *http.Response, error) {
	for _, s := range tc.requiredSubstrings {
		if !strings.Contains(query, s) {
			return datadog.MetricsQueryResponse{}, nil,
				fmt.Errorf("Required value %v not in query %v", s, query)
		}
	}
	if strings.HasPrefix(query, "kubernetes.cpu.usage") {
		for key, idx := range tc.cpuIndex {
			if strings.Contains(query, key) {
				ret := tc.retSequence[idx]
				ret.Query = &query
				return ret, nil, nil
			}
		}
	} else if strings.HasPrefix(query, "kubernetes.memory.usage") {
		for key, idx := range tc.memIndex {
			if strings.Contains(query, key) {
				ret := tc.retSequence[idx]
				ret.Query = &query
				return ret, nil, nil
			}
		}

	} else {
		return datadog.MetricsQueryResponse{}, nil,
			fmt.Errorf("Unknown Query %v in QueryMetrics", query)
	}
	return datadog.MetricsQueryResponse{}, nil,
		fmt.Errorf("Couldn't find a match for query (idx 2) in arg pack (%v,%v,%v).  Keys: %v", context, interval, query, tc.cpuIndex)
}

// Plan: verify that QueryMetrics is called appropriately for each query function we invoke.  Then make sure the
// resulting data is aggregated/split appropriately.
func TestDdclientPodMetrics_Get(t *testing.T) {
	doAssert := assert.New(t)
	client := testClient{}
	var err error
	client.addMetrics("banana", 100, 1.0, 1024, 5, map[string]string{
		"pod_name":          "banana",
		"kube_cluster_name": "test",
		"kube_namespace":    "foo",
		"container_name":    "fruit",
	})
	client.addMetrics("apple", 100, 1.5, 2048, 5, map[string]string{
		"pod_name":          "apple",
		"kube_cluster_name": "test",
		"kube_namespace":    "foo",
		"container_name":    "fruit",
	})
	client.requiredSubstrings = []string{" AND env:prod AND team:performance"}
	podMetrics := newDatadogClientWithFactory(time.Second, "test", fakeConfigFile, []string{"env:prod", "team:performance"}, func(config *datadog.Configuration) baseClient {
		return client
	})
	metrics := podMetrics.PodMetricses("foo")
	result, err := metrics.Get(context.TODO(), "banana", v1.GetOptions{})
	if err != nil {
		t.Errorf("Failed getting pod metrics for pod 'banana': %v", err)
	}
	doAssert.Equal("banana", result.Name)
	doAssert.Equal("foo", result.Namespace)
	doAssert.Equal(1, len(result.Containers))
	cpu := result.Containers[0].Usage.Cpu().ScaledValue(resource.Milli)
	doAssert.InDelta(cpu, 900, 1100)
	mem := result.Containers[0].Usage.Memory().ScaledValue(resource.Mega)
	doAssert.InDelta(mem, 500, 1500)

	result, err = metrics.Get(context.TODO(), "apple", v1.GetOptions{})
	if err != nil {
		t.Errorf("Failed getting pod metrics for pod 'banana': %v", err)
	}
	doAssert.Equal("apple", result.Name)
	doAssert.Equal("foo", result.Namespace)
	doAssert.Equal(1, len(result.Containers))
	cpu = result.Containers[0].Usage.Cpu().ScaledValue(resource.Milli)
	doAssert.InDelta(cpu, 1300, 1800)
	mem = result.Containers[0].Usage.Memory().ScaledValue(resource.Mega)
	doAssert.InDelta(mem, 1500, 2500)
}

type storedResults struct {
	baseClient
	Cpu, Mem datadog.MetricsQueryResponse
}

func (res *storedResults) QueryMetrics(context context.Context, interval time.Duration, query string) (datadog.MetricsQueryResponse, *http.Response, error) {
	// Nice and dumb response.  Split up the query by periods, use the second word to look up what to return.
	keywords := strings.Split(query, ".")
	if keywords[1] == "cpu" {
		return res.Cpu, nil, nil
	} else if keywords[1] == "memory" {
		return res.Mem, nil, nil
	} else {
		return datadog.MetricsQueryResponse{}, nil, fmt.Errorf("No match for %v", keywords[1])
	}
}

// Integration test.  Use recorded data from an API call and feed through.
// Use as a benchmark.
func TestDdclientPodMetrics_List(t *testing.T) {
	// 1. Load up some data JSON and pre-pump it through a test client.
	const filename = "testdata/combined-dd.json"
	dataset, err := os.Open(filename)
	if err != nil {
		t.Fatalf("Failed opening %s: %v", filename, err)
	}
	storedResponses := storedResults{}
	decoder := json.NewDecoder(dataset)
	err = decoder.Decode(&storedResponses)
	if err != nil {
		t.Fatalf("Failed parsing %s: %v", filename, err)
	}

	client := newDatadogClientWithFactory(10*time.Second, "general1", fakeConfigFile, []string{"env:prod", "team:performance"}, func(configuration *datadog.Configuration) baseClient {
		return &storedResponses
	})

	metricses := client.PodMetricses("")
	startTime := time.Now()
	res, err := metricses.List(context.TODO(), v1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed listing stored metrics: %v", err)
	}
	duration := time.Since(startTime)
	fmt.Printf(" ** Took %v to parse stored, nontrivial dataset of %v items **\n", duration, len(res.Items))
}
