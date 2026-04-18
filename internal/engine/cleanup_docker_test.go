package engine

import (
	"math"
	"testing"
)

func TestComputeDockerReclaimableMB(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantMB  float64
		wantOk  bool
		epsilon float64
	}{
		{
			name:   "invalid JSON returns ok=false",
			raw:    "not json at all",
			wantOk: false,
		},
		{
			name:    "empty response is valid and yields 0 MB",
			raw:     `{"Images":[],"BuildCache":[],"Containers":[],"Volumes":[]}`,
			wantMB:  0,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "running images contribute nothing",
			raw: `{
				"Images":[{"Size":1048576,"SharedSize":0,"Containers":2}],
				"BuildCache":[],"Containers":[],"Volumes":[]
			}`,
			wantMB:  0,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "unused image Size minus SharedSize",
			raw: `{
				"Images":[{"Size":10485760,"SharedSize":2097152,"Containers":0}],
				"BuildCache":[],"Containers":[],"Volumes":[]
			}`,
			// (10 MB - 2 MB) = 8 MB
			wantMB:  8,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "build cache reclaimable only when !InUse",
			raw: `{
				"Images":[],
				"BuildCache":[
					{"Size":1048576,"InUse":true},
					{"Size":5242880,"InUse":false}
				],
				"Containers":[],"Volumes":[]
			}`,
			// 5 MB
			wantMB:  5,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "stopped containers contribute SizeRw",
			raw: `{
				"Images":[],"BuildCache":[],
				"Containers":[
					{"State":"running","SizeRw":999999999},
					{"State":"exited","SizeRw":3145728}
				],
				"Volumes":[]
			}`,
			// 3 MB
			wantMB:  3,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "unused volumes contribute Size",
			raw: `{
				"Images":[],"BuildCache":[],"Containers":[],
				"Volumes":[
					{"UsageData":{"RefCount":0,"Size":2097152}},
					{"UsageData":{"RefCount":3,"Size":99999999}}
				]
			}`,
			// 2 MB
			wantMB:  2,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "Size < SharedSize clamped to 0",
			raw: `{
				"Images":[{"Size":1,"SharedSize":10485760,"Containers":0}],
				"BuildCache":[],"Containers":[],"Volumes":[]
			}`,
			wantMB:  0,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "sum of all four categories",
			raw: `{
				"Images":[{"Size":10485760,"SharedSize":0,"Containers":0}],
				"BuildCache":[{"Size":5242880,"InUse":false}],
				"Containers":[{"State":"exited","SizeRw":3145728}],
				"Volumes":[{"UsageData":{"RefCount":0,"Size":2097152}}]
			}`,
			// 10 + 5 + 3 + 2 = 20 MB
			wantMB:  20,
			wantOk:  true,
			epsilon: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := computeDockerReclaimableMB([]byte(tt.raw))
			if ok != tt.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if math.Abs(got-tt.wantMB) > tt.epsilon {
				t.Errorf("MB = %v, want %v (±%v)", got, tt.wantMB, tt.epsilon)
			}
		})
	}
}
