package recommender

import (
	"fmt"
	"github.com/DataDog/datadog-go/v5/statsd"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"testing"
	"time"
)

const (
	typeHistogram = "hist"
	typeGauge     = "gauge"
)

type record struct {
	callType string
	name     string
	value    float64
	tags     []string
	rate     float64
}

type recordingClient struct {
	records []record
}

func (rc *recordingClient) IsClosed() bool {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) GetTelemetry() statsd.Telemetry {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Count(name string, value int64, tags []string, rate float64) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Distribution(name string, value float64, tags []string, rate float64) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Decr(name string, tags []string, rate float64) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Incr(name string, tags []string, rate float64) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Set(name string, value string, tags []string, rate float64) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Timing(name string, value time.Duration, tags []string, rate float64) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) TimeInMilliseconds(name string, value float64, tags []string, rate float64) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Event(e *statsd.Event) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) SimpleEvent(title, text string) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) ServiceCheck(sc *statsd.ServiceCheck) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) SimpleServiceCheck(name string, status statsd.ServiceCheckStatus) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Close() error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Flush() error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) SetWriteTimeout(d time.Duration) error {
	panic("unexpected API Call in test.")
}

func (rc *recordingClient) Histogram(name string, value float64, tags []string, rate float64) error {
	rc.records = append(rc.records, record{typeHistogram, name, value, tags, rate})
	return nil
}

func (rc *recordingClient) Gauge(name string, value float64, tags []string, rate float64) error {
	rc.records = append(rc.records, record{typeGauge, name, value, tags, rate})
	return nil
}

// FilteredSum will take any non-nil arguments and use them to filter all records, then sum the value field.
func (rc *recordingClient) FilteredSum(name string, tags []string) float64 {
	total := 0.0
	for _, rec := range rc.records {
		accept := true
		if name != "" {
			accept = name == rec.name
		}
		if accept && len(tags) > 0 {
			for _, t := range tags {
				accept = false
				for _, rt := range rec.tags {
					if rt == t {
						accept = true
					}
				}
			}
		}
		if accept {
			total += rec.value
		}
	}
	return total
}

func makeResources(milliCores int64, mib int64) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", milliCores)),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", mib)),
	}
}

func TestFinishScan(t *testing.T) {
	client := recordingClient{make([]record, 0, 50)}
	vpaLabels := map[string]string{"team": "secondTeamVPA"}
	pod1Labels := map[string]string{"team": "firstTeam", "app": "firstApp", "pod_name": "firstPod"}
	pod2Labels := map[string]string{"team": "secondTeam", "app": "secondApp", "pod_name": "secondPod"}
	Register(&client, []string{"team", "app"})
	// Begin a session to record values.
	StartScan()
	RecordUnmatchedPod(pod1Labels)
	RecordMatchedPod(pod2Labels, labels.Set(vpaLabels))
	RecordContainerRequestDiff("first_c", "", "firstPod", labels.Set(pod1Labels), labels.Set(vpaLabels),
		makeResources(500, 128), makeResources(250, 512))
	RecordPodRequestDiff("", "firstPod", labels.Set(pod1Labels), labels.Set(vpaLabels),
		map[string]corev1.ResourceList{"first_c": makeResources(500, 128)}, // vpa target
		map[string]corev1.ResourceList{"first_c": makeResources(250, 512)}) // pod request
	FinishScan()
	// Now validate the recorded values.
	for nr, v := range client.records {
		fmt.Printf("%d: %s %s %f %v %f\n", nr+1, v.callType, v.name, v.value, v.tags, v.rate)
	}

	if total := client.FilteredSum(metricVpaMatchedCount, []string{}); total != 1.0 {
		t.Fatal("Matched pod count is wrong, should be 1, got ", total)
	}

	if total := client.FilteredSum(metricVpaUnmatchedCount, []string{}); total != 1.0 {
		t.Fatal("Unmatched pod count is wrong, should be 1, got ", total)
	}

	if total := client.FilteredSum(metricPodDiffCores, []string{"pod_name:firstPod"}); total < 0.1 || total > 1.0 {
		t.Fatal("Pod diff is wrong.  Expected .25 cores, got ", total)
	}

	if total := client.FilteredSum(metricPodDiffMib, []string{"pod_name:firstPod"}); total != -403 {
		t.Fatal("Pod diff is wrong.  Expected -403 MiB, got ", total)
	}
}
