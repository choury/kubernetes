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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/apis/core"
	v1qos "k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
	"k8s.io/kubernetes/pkg/kubelet/status"
)

// PolicyAffiliate is the name of the affiliate policy
const PolicyAffiliate policyName = "affiliate"

// affiliatePolicy is a CPU manager policy that does not change CPU
type affiliatePolicy struct {
	// cpu socket topology
	topology *topology.CPUTopology
	// set of CPUs that is not available for exclusive assignment
	reserved          cpuset.CPUSet
	podStatusProvider status.PodStatusProvider
	// a map of affiliate container and the container affiliated
	affiliated map[string]affiliateInfo
}

type affiliateInfo struct {
	uid          types.UID
	containerIDs []string
}

// Ensure affiliatePolicy implements Policy interface
var _ Policy = &affiliatePolicy{}

// NewAffiliatePolicy returns a CPU manager policy that does not change CPU
// assignments for exclusively pinned guaranteed containers after the main
// container process starts.
func NewAffiliatePolicy(topology *topology.CPUTopology, numReservedCPUs int) Policy {
	allCPUs := topology.CPUDetails.CPUs()
	// takeByTopology allocates CPUs associated with low-numbered cores from
	// allCPUs.
	//
	// For example: Given a system with 8 CPUs available and HT enabled,
	// if numReservedCPUs=2, then reserved={0,4}
	reserved, _ := takeByTopology(topology, allCPUs, numReservedCPUs)

	if reserved.Size() != numReservedCPUs {
		panic(fmt.Sprintf("[cpumanager] unable to reserve the required amount of CPUs (size of %s did not equal %d)", reserved, numReservedCPUs))
	}

	klog.Infof("[cpumanager] reserved %d CPUs (\"%s\") not available for exclusive assignment", reserved.Size(), reserved)

	return &affiliatePolicy{
		topology:   topology,
		reserved:   reserved,
		affiliated: make(map[string]affiliateInfo),
	}
}

func (p *affiliatePolicy) Name() string {
	return string(PolicyAffiliate)
}

func (p *affiliatePolicy) Start(s state.State, podStatusProvider status.PodStatusProvider) {
	if err := p.validateState(s); err != nil {
		klog.Errorf("[cpumanager] affiliate policy invalid state: %s\n", err.Error())
		panic("[cpumanager] - please drain node and remove policy state file")
	}
	p.podStatusProvider = podStatusProvider
}

func (p *affiliatePolicy) validateState(s state.State) error {
	tmpAssignments := s.GetCPUAssignments()
	tmpDefaultCPUset := s.GetDefaultCPUSet()

	// Default cpuset cannot be empty when assignments exist
	if tmpDefaultCPUset.IsEmpty() {
		if len(tmpAssignments) != 0 {
			return fmt.Errorf("default cpuset cannot be empty")
		}
		// state is empty initialize
		allCPUs := p.topology.CPUDetails.CPUs()
		s.SetDefaultCPUSet(allCPUs)
		return nil
	}

	// State has already been initialized from file (is not empty)
	// 1 Check if the reserved cpuset is not part of default cpuset because:
	// - kube/system reserved have changed (increased) - may lead to some containers not being able to start
	// - user tampered with file
	if !p.reserved.Intersection(tmpDefaultCPUset).Equals(p.reserved) {
		return fmt.Errorf("not all reserved cpus: \"%s\" are present in defaultCpuSet: \"%s\"",
			p.reserved.String(), tmpDefaultCPUset.String())
	}

	// 2. Check if state for affiliate policy is consistent
	for cID, cset := range tmpAssignments {
		// None of the cpu in DEFAULT cset should be in s.assignments
		if !tmpDefaultCPUset.Intersection(cset).IsEmpty() {
			return fmt.Errorf("container id: %s cpuset: \"%s\" overlaps with default cpuset \"%s\"",
				cID, cset.String(), tmpDefaultCPUset.String())
		}
	}
	return nil
}

// assignableCPUs returns the set of unassigned CPUs minus the reserved set.
func (p *affiliatePolicy) assignableCPUs(s state.State) cpuset.CPUSet {
	return s.GetDefaultCPUSet().Difference(p.reserved)
}

// assignAffiliatedPod will set cpuset for affiliate pod
func (p *affiliatePolicy) assignAffiliatePod(s state.State, containerID string, affUID types.UID) {
	affInfo := affiliateInfo{
		uid: affUID,
	}
	cpuset := cpuset.NewCPUSet()
	if podStatus, ok := p.podStatusProvider.GetPodStatus(affUID); ok {
		for _, cStatus := range podStatus.ContainerStatuses {
			cid := &kubecontainer.ContainerID{}
			if err := cid.ParseString(cStatus.ContainerID); err != nil {
				klog.Fatalf("parse container id error: %v", err)
			}
			if cset, ok := s.GetCPUSet(cid.ID); !ok {
				klog.Warningf("[cpumanager] affiliate policy: Affiliated container not found: %v", cid.ID)
			} else {
				cpuset = cpuset.Union(cset)
				affInfo.containerIDs = append(affInfo.containerIDs, cid.ID)
			}
		}
	}
	if !cpuset.IsEmpty() {
		s.SetCPUSet(containerID, cpuset)
	} else {
		//The affiliated pod may not running or deleted
		s.SetCPUSet(containerID, p.assignableCPUs(s))
	}
	p.affiliated[containerID] = affInfo
}

func (p *affiliatePolicy) AddContainer(s state.State, pod *v1.Pod, container *v1.Container, containerID string) error {
	if _, ok := s.GetCPUSet(containerID); ok {
		// container belongs in an exclusively allocated pool
		klog.Infof("[cpumanager] affiliate policy: container already present in state, skipping (container: %s, container id: %s)", container.Name, containerID)
		return nil
	}

	if numCPUs := requiredCPUs(pod, container); numCPUs != 0 {
		klog.Infof("[cpumanager] affiliate policy: AddContainer (pod: %s, container: %s, container id: %s)", pod.Name, container.Name, containerID)
		cpuset, err := p.allocateCPUs(s, numCPUs)
		if err != nil {
			klog.Errorf("[cpumanager] unable to allocate %d CPUs (container id: %s, error: %v)", numCPUs, containerID, err)
			return err
		}
		s.SetCPUSet(containerID, cpuset)
		// resign affiliate pod cpuset if this pod readded.
		for affiliateContainer, affInfo := range p.affiliated {
			if affInfo.uid != pod.UID {
				continue
			}
			p.assignAffiliatePod(s, affiliateContainer, pod.UID)
		}
	}
	if v1qos.IsTencentAffiliatePod(pod.Annotations) {
		if _, ok := p.affiliated[containerID]; ok {
			return fmt.Errorf("Affiliate container %s has already exists", containerID)
		}
		affPod, found := pod.Annotations[core.TencentAffiliatedAnnotationKey]
		if !found {
			// affiliate pod can't use reserved cpu
			s.SetCPUSet(containerID, p.assignableCPUs(s))
			p.affiliated[containerID] = affiliateInfo{}
			return nil
		}
		p.assignAffiliatePod(s, containerID, types.UID(affPod))
	}
	// container belongs in the shared pool (nothing to do; use default cpuset)
	return nil
}

func (p *affiliatePolicy) RemoveContainer(s state.State, containerID string) error {
	klog.Infof("[cpumanager] affiliate policy: RemoveContainer (container id: %s)", containerID)
	if toRelease, ok := s.GetCPUSet(containerID); ok {
		s.Delete(containerID)
		// this is a Affiliated  pod, we will not release to default.
		if _, ok := p.affiliated[containerID]; ok {
			delete(p.affiliated, containerID)
			return nil
		}
		// Mutate the shared pool, adding released cpus.
		s.SetDefaultCPUSet(s.GetDefaultCPUSet().Union(toRelease))
		// we found the affiliate pod that affiliated on it, and release them
		for affiliateContainer, affInfo := range p.affiliated {
			leftContainer := []string{}
			for _, c := range affInfo.containerIDs {
				if c != containerID {
					leftContainer = append(leftContainer, c)
					continue
				}
				cset, ok := s.GetCPUSet(affiliateContainer)
				if !ok {
					return fmt.Errorf("affiliate container not found in state: %s", affiliateContainer)
				}
				cset = cset.Difference(toRelease)
				if !cset.IsEmpty() {
					s.SetCPUSet(affiliateContainer, cset)
				}
			}
			p.affiliated[affiliateContainer] = affiliateInfo{
				uid:          affInfo.uid,
				containerIDs: leftContainer,
			}
			if len(leftContainer) == 0 {
				s.SetCPUSet(affiliateContainer, p.assignableCPUs(s))
			}
		}
	}
	return nil
}

func (p *affiliatePolicy) allocateCPUs(s state.State, numCPUs int) (cpuset.CPUSet, error) {
	klog.Infof("[cpumanager] allocateCpus: (numCPUs: %d)", numCPUs)
	result, err := takeByTopology(p.topology, p.assignableCPUs(s), numCPUs)
	if err != nil {
		return cpuset.NewCPUSet(), err
	}
	// Remove allocated CPUs from the shared CPUSet.
	s.SetDefaultCPUSet(s.GetDefaultCPUSet().Difference(result))

	klog.Infof("[cpumanager] allocateCPUs: returning \"%v\"", result)
	return result, nil
}

func requiredCPUs(pod *v1.Pod, container *v1.Container) int {
	if v1qos.IsTencentFatPod(pod.Annotations) {
		cpuQuantity := container.Resources.Limits[v1.ResourceCPU]
		if cpuQuantity.Value()*1000 != cpuQuantity.MilliValue() {
			return 0
		}
		return int(cpuQuantity.Value())
	}
	if v1qos.IsTencentAffiliatePod(pod.Annotations) {
		return 0
	}
	return guaranteedCPUs(pod, container)
}
