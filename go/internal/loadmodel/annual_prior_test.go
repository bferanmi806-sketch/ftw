package loadmodel

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestAnnualPriorLeavesLearningIntact(t *testing.T) {
	s := NewService(nil, nil, "meter", 5000, 0)
	annual := 8760.0
	s.SetAnnualBase(annual)
	m := s.Model()
	mean := 0.0
	start := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for i := 0; i < Buckets; i++ {
		mean += m.PredictNoTemp(start.Add(time.Duration(i)*time.Hour)) / Buckets
	}
	if math.Abs(mean-1000) > 1e-8 || m.Samples != 0 {
		t.Fatalf("annual prior mean=%v samples=%v", mean, m.Samples)
	}
	trained := s.models[ProfileHome]
	for i := range trained.Bucket {
		trained.Bucket[i].Mean = 750
		trained.Bucket[i].Days = MinTrustSamples
		trained.Bucket[i].Samples = 100
	}
	trained.Samples = 1000
	trained.ConfigRevision = "physical-source-1"
	before := *trained
	s.SetAnnualBase(17520)
	after := s.Model()
	if !reflect.DeepEqual(before.Bucket, after.Bucket) || before.Samples != after.Samples || before.ConfigRevision != after.ConfigRevision {
		t.Fatal("changing prior changed measured learning")
	}
	if after.PredictNoTemp(start) != before.PredictNoTemp(start) {
		t.Fatal("prior changed a fully learned bucket")
	}
	s.SetAnnualBase(0)
	if s.Model().PriorScale != 1 {
		t.Fatal("removing prior did not restore generic estimate")
	}
}
