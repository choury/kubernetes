/*
Copyright 2017 The Kubernetes Authors.

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

package cpumanager

import (
	"fmt"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/apis/core"
	v1qos "k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"k8s.io/kubernetes/pkg/kubelet/status"
)

type affiliatePolicyTest struct {
	description       string
	topo              *topology.CPUTopology
	numReservedCPUs   int
	containerID       string
	stAssignments     state.ContainerCPUAssignments
	stDefaultCPUSet   cpuset.CPUSet
	pod               *v1.Pod
	expErr            error
	expCPUAlloc       bool
	expCSet           cpuset.CPUSet
	expPanic          bool
	podStatusProvider status.PodStatusProvider
}

func TestAffiliatePolicyName(t *testing.T) {
	policy := NewAffiliatePolicy(topoSingleSocketHT, 1)

	policyName := policy.Name()
	if policyName != "affiliate" {
		t.Errorf("AffiliatePolicy Name() error. expected: affiliate, returned: %v",
			policyName)
	}
}

func makePodProvider(ids ...string) status.PodStatusProvider {
	cStatus := make([]v1.ContainerStatus, 0, len(ids))
	for _, id := range ids {
		cStatus = append(cStatus, v1.ContainerStatus{
			ContainerID: fmt.Sprintf("docker://%s", id),
		})
	}
	return &mockPodStatusProvider{
		podStatus: v1.PodStatus{
			ContainerStatuses: cStatus,
		},
		found: true,
	}
}

func makeAffliatedPod(cpuRequest, cpuLimit, affliated string) *v1.Pod {
	pod := makePod(cpuRequest, cpuLimit)
	annotations := map[string]string{
		core.TencentPodTypeAnnotationKey: v1qos.TencentPodTypeAffiliate,
	}
	if affliated != "" {
		annotations[core.TencentAffiliatedAnnotationKey] = affliated
	}
	pod.Annotations = annotations
	return pod
}

func affiliateMakePod(cpuRequest, cpuLimit string, podName string, annotations map[string]string, names ...string) *v1.Pod {
	containers := make([]v1.Container, 0, len(names))
	for _, name := range names {
		containers = append(containers, v1.Container{
			Name: name,
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceName(v1.ResourceCPU):    resource.MustParse(cpuRequest),
					v1.ResourceName(v1.ResourceMemory): resource.MustParse("1G"),
				},
				Limits: v1.ResourceList{
					v1.ResourceName(v1.ResourceCPU):    resource.MustParse(cpuLimit),
					v1.ResourceName(v1.ResourceMemory): resource.MustParse("1G"),
				},
			},
		})
	}
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			UID:         types.UID(podName),
			Annotations: annotations,
		},
		Spec: v1.PodSpec{
			Containers: containers,
		},
	}
}

func TestAffiliatePolicyAdd(t *testing.T) {
	largeTopoBuilder := cpuset.NewBuilder()
	largeTopoSock0Builder := cpuset.NewBuilder()
	largeTopoSock1Builder := cpuset.NewBuilder()
	largeTopo := *topoQuadSocketFourWayHT
	for cpuid, val := range largeTopo.CPUDetails {
		largeTopoBuilder.Add(cpuid)
		if val.SocketID == 0 {
			largeTopoSock0Builder.Add(cpuid)
		} else if val.SocketID == 1 {
			largeTopoSock1Builder.Add(cpuid)
		}
	}

	testCases := []affiliatePolicyTest{
		{
			description:     "fat pod, no resource",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID2",
			stAssignments:   state.ContainerCPUAssignments{},
			stDefaultCPUSet: cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			pod: affiliateMakePod("1000m", "8000m", "Pod01", map[string]string{
				core.TencentPodTypeAnnotationKey: "fat",
			}, "fakeID2"),
			expErr:            fmt.Errorf("not enough cpus available to satisfy request"),
			expCPUAlloc:       false,
			expCSet:           cpuset.NewCPUSet(),
			podStatusProvider: mockPodStatusProvider{},
		},
		{
			description:     "fat pod, no error",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID2",
			stAssignments:   state.ContainerCPUAssignments{},
			stDefaultCPUSet: cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			pod: affiliateMakePod("1000m", "7000m", "Pod01", map[string]string{
				core.TencentPodTypeAnnotationKey: "fat",
			}, "fakeID2"),
			expErr:            nil,
			expCPUAlloc:       true,
			expCSet:           cpuset.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
			podStatusProvider: mockPodStatusProvider{},
		},
		{
			description:       "No exist affiated pod, use default pool",
			topo:              topoSingleSocketHT,
			numReservedCPUs:   1,
			containerID:       "fakeID2",
			stAssignments:     state.ContainerCPUAssignments{},
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			pod:               makeAffliatedPod("1000m", "2000m", "p1"),
			expErr:            nil,
			expCPUAlloc:       true,
			expCSet:           cpuset.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
			podStatusProvider: mockPodStatusProvider{},
		},
		{
			description:       "No exist affiated container, return default",
			topo:              topoSingleSocketHT,
			numReservedCPUs:   1,
			containerID:       "fakeID2",
			stAssignments:     state.ContainerCPUAssignments{},
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			pod:               makeAffliatedPod("1000m", "2000m", "p1"),
			expErr:            nil,
			expCPUAlloc:       true,
			expCSet:           cpuset.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
			podStatusProvider: makePodProvider("fakeID100"),
		},
		{
			description:     "Reuse affiated pod cpuset",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID2",
			stAssignments: state.ContainerCPUAssignments{
				"fakeID100": cpuset.NewCPUSet(1, 2),
				"fakeID200": cpuset.NewCPUSet(3, 4),
			},
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 5, 6, 7),
			pod:               makeAffliatedPod("1000m", "2000m", "p1"),
			expErr:            nil,
			expCPUAlloc:       true,
			expCSet:           cpuset.NewCPUSet(1, 2, 3, 4),
			podStatusProvider: makePodProvider("fakeID100", "fakeID200"),
		},
		{
			description:     "affiate pod can't use reserved cpu",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID2",
			stAssignments: state.ContainerCPUAssignments{
				"fakeID100": cpuset.NewCPUSet(1, 2),
				"fakeID200": cpuset.NewCPUSet(3, 4),
			},
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 5, 6, 7),
			pod:               makeAffliatedPod("1000m", "2000m", ""),
			expErr:            nil,
			expCPUAlloc:       true,
			expCSet:           cpuset.NewCPUSet(5, 6, 7),
			podStatusProvider: makePodProvider("fakeID100", "fakeID200"),
		},
	}

	for _, testCase := range testCases {
		policy := NewAffiliatePolicy(testCase.topo, testCase.numReservedCPUs)

		st := &mockState{
			assignments:   testCase.stAssignments,
			defaultCPUSet: testCase.stDefaultCPUSet,
		}
		policy.Start(st, testCase.podStatusProvider)

		container := &testCase.pod.Spec.Containers[0]
		err := policy.AddContainer(st, testCase.pod, container, testCase.containerID)
		if !reflect.DeepEqual(err, testCase.expErr) {
			t.Errorf("AffiliatePolicy AddContainer() error (%v). expected add error: %v but got: %v",
				testCase.description, testCase.expErr, err)
		}

		if testCase.expCPUAlloc {
			cset, found := st.assignments[testCase.containerID]
			if !found {
				t.Errorf("AffiliatePolicy AddContainer() error (%v). expected container id %v to be present in assignments %v",
					testCase.description, testCase.containerID, st.assignments)
			}

			if !reflect.DeepEqual(cset, testCase.expCSet) {
				t.Errorf("AffiliatePolicy AddContainer() error (%v). expected cpuset %v but got %v",
					testCase.description, testCase.expCSet, cset)
			}

			if !v1qos.IsTencentAffiliatePod(testCase.pod.Annotations) && !cset.Intersection(st.defaultCPUSet).IsEmpty() {
				t.Errorf("AffiliatePolicy AddContainer() error (%v). expected cpuset %v to be disoint from the shared cpuset %v",
					testCase.description, cset, st.defaultCPUSet)
			}
		}

		if !testCase.expCPUAlloc {
			_, found := st.assignments[testCase.containerID]
			if found {
				t.Errorf("AffiliatePolicy AddContainer() error (%v). Did not expect container id %v to be present in assignments %v",
					testCase.description, testCase.containerID, st.assignments)
			}
		}
	}
}

type affiliatePolicyRemoveTest struct {
	description       string
	topo              *topology.CPUTopology
	numReservedCPUs   int
	pods              []*v1.Pod
	containerID       string
	stDefaultCPUSet   cpuset.CPUSet
	expCSet           cpuset.CPUSet
	expAssignment     state.ContainerCPUAssignments
	podStatusProvider affiliatePodStatusProvider
}

type affiliatePodStatusProvider map[string]v1.PodStatus

func (t affiliatePodStatusProvider) addPod(podUID string, ids ...string) {
	cStatus := make([]v1.ContainerStatus, 0, len(ids))
	for _, id := range ids {
		cStatus = append(cStatus, v1.ContainerStatus{
			ContainerID: fmt.Sprintf("docker://%s", id),
		})
	}
	t[podUID] = v1.PodStatus{
		ContainerStatuses: cStatus,
	}
}

func (t affiliatePodStatusProvider) GetPodStatus(uid types.UID) (v1.PodStatus, bool) {
	p, ok := t[string(uid)]
	return p, ok
}

func TestAffiliatePolicyRemove(t *testing.T) {
	testCases := []affiliatePolicyRemoveTest{
		{
			description:     "SingleSocketHT, normal pod",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID1",
			pods: []*v1.Pod{
				affiliateMakePod("2", "2", "Pod01", nil, "fakeID1"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expAssignment:     state.ContainerCPUAssignments{},
		},
		{
			description:     "SingleSocketHT, fat pod and normal pod",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID2",
			pods: []*v1.Pod{
				affiliateMakePod("1", "2", "Pod01", map[string]string{
					core.TencentPodTypeAnnotationKey: "fat",
				}, "fakeID1"),
				affiliateMakePod("1", "2", "Pod01", nil, "fakeID2"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 2, 3, 4, 6, 7),
			expAssignment: state.ContainerCPUAssignments{
				"fakeID1": cpuset.NewCPUSet(1, 5),
			},
		},
		{
			description:     "SingleSocketHT, fat pod with multiple containers",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID2",
			pods: []*v1.Pod{
				affiliateMakePod("1", "2", "Pod01", map[string]string{
					core.TencentPodTypeAnnotationKey: "fat",
				}, "fakeID1", "fakeID2"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 2, 3, 4, 6, 7),
			expAssignment: state.ContainerCPUAssignments{
				"fakeID1": cpuset.NewCPUSet(1, 5),
			},
		},
		{
			description:     "SingleSocketHT, affliate pod",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID3",
			pods: []*v1.Pod{
				affiliateMakePod("1", "2", "Pod01", map[string]string{
					core.TencentPodTypeAnnotationKey: "fat",
				}, "fakeID1", "fakeID2"),
				affiliateMakePod("1", "2", "Pod02", map[string]string{
					core.TencentPodTypeAnnotationKey: "affiliate",
				}, "fakeID3"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 3, 4, 7),
			expAssignment: state.ContainerCPUAssignments{
				"fakeID1": cpuset.NewCPUSet(1, 5),
				"fakeID2": cpuset.NewCPUSet(2, 6),
			},
		},
		{
			description:     "SingleSocketHT, affliate pod2",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID1",
			pods: []*v1.Pod{
				affiliateMakePod("1", "2", "Pod01", map[string]string{
					core.TencentPodTypeAnnotationKey: "fat",
				}, "fakeID1", "fakeID2"),
				affiliateMakePod("1", "2", "Pod02", map[string]string{
					core.TencentPodTypeAnnotationKey: "affiliate",
				}, "fakeID3"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 1, 3, 4, 5, 7),
			expAssignment: state.ContainerCPUAssignments{
				"fakeID2": cpuset.NewCPUSet(2, 6),
				"fakeID3": cpuset.NewCPUSet(1, 3, 4, 5, 7),
			},
		},
		{
			description:     "SingleSocketHT, affliate pod3",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID1",
			pods: []*v1.Pod{
				affiliateMakePod("1", "2", "Pod01", map[string]string{
					core.TencentPodTypeAnnotationKey: "fat",
				}, "fakeID1", "fakeID2"),
				affiliateMakePod("1", "2", "Pod02", map[string]string{
					core.TencentPodTypeAnnotationKey:    "affiliate",
					core.TencentAffiliatedAnnotationKey: "NoPod",
				}, "fakeID3"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 1, 3, 4, 5, 7),
			expAssignment: state.ContainerCPUAssignments{
				"fakeID2": cpuset.NewCPUSet(2, 6),
				"fakeID3": cpuset.NewCPUSet(1, 3, 4, 5, 7),
			},
		},
		{
			description:     "SingleSocketHT, affliate pod, release one fat",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID1",
			pods: []*v1.Pod{
				affiliateMakePod("1", "2", "Pod01", map[string]string{
					core.TencentPodTypeAnnotationKey: "fat",
				}, "fakeID1", "fakeID2"),
				affiliateMakePod("1", "2", "Pod02", map[string]string{
					core.TencentPodTypeAnnotationKey:    "affiliate",
					core.TencentAffiliatedAnnotationKey: "Pod01",
				}, "fakeID3"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 1, 3, 4, 5, 7),
			expAssignment: state.ContainerCPUAssignments{
				"fakeID2": cpuset.NewCPUSet(2, 6),
				"fakeID3": cpuset.NewCPUSet(2, 6),
			},
		},
		{
			description:     "SingleSocketHT, affliate pod, release all fat",
			topo:            topoSingleSocketHT,
			numReservedCPUs: 1,
			containerID:     "fakeID1",
			pods: []*v1.Pod{
				affiliateMakePod("1", "2", "Pod01", map[string]string{
					core.TencentPodTypeAnnotationKey: "fat",
				}, "fakeID1"),
				affiliateMakePod("1", "2", "Pod02", map[string]string{
					core.TencentPodTypeAnnotationKey:    "affiliate",
					core.TencentAffiliatedAnnotationKey: "Pod01",
				}, "fakeID2"),
			},
			podStatusProvider: make(affiliatePodStatusProvider),
			stDefaultCPUSet:   cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expCSet:           cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			expAssignment: state.ContainerCPUAssignments{
				"fakeID2": cpuset.NewCPUSet(1, 2, 3, 4, 5, 6, 7),
			},
		},
	}

	for _, testCase := range testCases {
		policy := NewAffiliatePolicy(testCase.topo, testCase.numReservedCPUs)

		st := &mockState{
			assignments:   state.ContainerCPUAssignments{},
			defaultCPUSet: testCase.stDefaultCPUSet,
		}
		policy.Start(st, testCase.podStatusProvider)
		for _, pod := range testCase.pods {
			ids := []string{}
			for i, c := range pod.Spec.Containers {
				policy.AddContainer(st, pod, &pod.Spec.Containers[i], c.Name)
				ids = append(ids, c.Name)
			}
			testCase.podStatusProvider.addPod(pod.Name, ids...)
		}
		policy.RemoveContainer(st, testCase.containerID)

		if !reflect.DeepEqual(st.defaultCPUSet, testCase.expCSet) {
			t.Errorf("AffiliatePolicy RemoveContainer() error (%v). expected default cpuset %v but got %v",
				testCase.description, testCase.expCSet, st.defaultCPUSet)
		}

		if _, found := st.assignments[testCase.containerID]; found {
			t.Errorf("AffiliatePolicy RemoveContainer() error (%v). expected containerID %v not be in assignments %v",
				testCase.description, testCase.containerID, st.assignments)
		}
		for c, cpuset := range st.assignments {
			if !cpuset.Equals(testCase.expAssignment[c]) {
				t.Errorf("AffiliatePolicy RemoveContainer() error (%v). expected containerID %v not same in assignments %v",
					testCase.description, testCase.containerID, st.assignments)
			}
		}
		for c, cpuset := range testCase.expAssignment {
			if !cpuset.Equals(st.assignments[c]) {
				t.Errorf("AffiliatePolicy RemoveContainer() error (%v). expected containerID %v not same in assignments %v",
					testCase.description, testCase.containerID, st.assignments)
			}
		}
	}
}

func TestAffiliatePolicyReAdd(t *testing.T) {
	policy := NewAffiliatePolicy(topoSingleSocketHT, 1)

	st := &mockState{
		assignments:   state.ContainerCPUAssignments{},
		defaultCPUSet: cpuset.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
	}
	podStatusProvider := make(affiliatePodStatusProvider)
	podStatusProvider.addPod("Pod01", "fakeID1")
	podStatusProvider.addPod("Pod02", "fakeID2")
	policy.Start(st, podStatusProvider)
	pod := affiliateMakePod("1", "2", "Pod01", map[string]string{
		core.TencentPodTypeAnnotationKey:    "affiliate",
		core.TencentAffiliatedAnnotationKey: "Pod02",
	}, "fakeID1")

	for i, c := range pod.Spec.Containers {
		if err := policy.AddContainer(st, pod, &pod.Spec.Containers[i], c.Name); err != nil {
			t.Errorf("AffiliatePolicy AddContainer error: %v", err)
		}
	}
	podStatusProvider.addPod("Pod01", "fakeID1")

	pod = affiliateMakePod("2", "2", "Pod02", nil, "fakeID2")
	for i, c := range pod.Spec.Containers {
		if err := policy.AddContainer(st, pod, &pod.Spec.Containers[i], c.Name); err != nil {
			t.Errorf("AffiliatePolicy AddContainer error: %v", err)
		}
	}

	if !reflect.DeepEqual(st.defaultCPUSet, cpuset.NewCPUSet(0, 2, 3, 4, 6, 7)) {
		t.Errorf("AffiliatePolicy Readded error. get default cpuset %v", st.defaultCPUSet)
	}
	if !st.assignments["fakeID2"].Equals(st.assignments["fakeID1"]) {
		t.Errorf("AffiliatePolicy Readded error. assignments %v", st.assignments)
	}
}
