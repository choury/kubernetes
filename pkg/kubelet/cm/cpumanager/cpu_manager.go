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
	"math"
	"sync"
	"time"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"

	runtimeapi "k8s.io/kubernetes/pkg/kubelet/apis/cri/runtime/v1alpha2"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"k8s.io/kubernetes/pkg/kubelet/config"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
	"k8s.io/kubernetes/pkg/kubelet/status"
)

// ActivePodsFunc is a function that returns a list of pods to reconcile.
type ActivePodsFunc func() []*v1.Pod

type runtimeService interface {
	UpdateContainerResources(id string, resources *runtimeapi.LinuxContainerResources) error
}

type policyName string

// cpuManagerStateFileName is the file name where cpu manager stores its state
const cpuManagerStateFileName = "cpu_manager_state"

// Manager interface provides methods for Kubelet to manage pod cpus.
type Manager interface {
	// Start is called during Kubelet initialization.
	Start(activePods ActivePodsFunc, sourcesReady config.SourcesReady, podStatusProvider status.PodStatusProvider, containerRuntime runtimeService)

	// AddContainer is called between container create and container start
	// so that initial CPU affinity settings can be written through to the
	// container runtime before the first process begins to execute.
	AddContainer(p *v1.Pod, c *v1.Container, containerID string) error

	// UpdateContainer is called after container's resources has been changed.
	// It will firstly release the CPUs allocated to the container, and then
	// reallocate some for it as AddContainer do.
	UpdateContainer(p *v1.Pod, c *v1.Container, containerID string) error
	// RemoveContainer is called after Kubelet decides to kill or delete a
	// container. After this call, the CPU manager stops trying to reconcile
	// that container and any CPUs dedicated to the container are freed.
	RemoveContainer(containerID string) error

	// State returns a read-only interface to the internal CPU manager state.
	State() state.Reader
}

type manager struct {
	sync.Mutex
	policy Policy

	// reconcilePeriod is the duration between calls to reconcileState.
	reconcilePeriod time.Duration

	// state allows pluggable CPU assignment policies while sharing a common
	// representation of state for the system to inspect and reconcile.
	state state.State

	// containerRuntime is the container runtime service interface needed
	// to make UpdateContainerResources() calls against the containers.
	containerRuntime runtimeService

	// activePods is a method for listing active pods on the node
	// so all the containers can be updated in the reconciliation loop.
	activePods ActivePodsFunc

	// podStatusProvider provides a method for obtaining pod statuses
	// and the containerID of their containers
	podStatusProvider status.PodStatusProvider

	machineInfo *cadvisorapi.MachineInfo

	nodeAllocatableReservation v1.ResourceList

	// sourcesReady provides the readiness of kubelet configuration sources such as apiserver update readiness.
	// We use it to determine when we can purge inactive pods from checkpointed state.
	sourcesReady config.SourcesReady
}

var _ Manager = &manager{}

type sourcesReadyStub struct{}

func (s *sourcesReadyStub) AddSource(source string) {}
func (s *sourcesReadyStub) AllReady() bool          { return true }

// NewManager creates new cpu manager based on provided policy
func NewManager(cpuPolicyName string, reconcilePeriod time.Duration, machineInfo *cadvisorapi.MachineInfo, nodeAllocatableReservation v1.ResourceList, stateFileDirectory string, cpuReservedEnabled bool) (Manager, error) {
	var policy Policy

	topoFunc := func() (*topology.CPUTopology, int, error) {
		topo, err := topology.Discover(machineInfo)
		if err != nil {
			return nil, 0, err
		}
		klog.Infof("[cpumanager] detected CPU topology: %v", topo)
		reservedCPUs, ok := nodeAllocatableReservation[v1.ResourceCPU]
		if !ok {
			// The static policy cannot initialize without this information.
			return nil, 0, fmt.Errorf("[cpumanager] unable to determine reserved CPU resources for static policy")
		}
		if reservedCPUs.IsZero() {
			// The static policy requires this to be nonzero. Zero CPU reservation
			// would allow the shared pool to be completely exhausted. At that point
			// either we would violate our guarantee of exclusivity or need to evict
			// any pod that has at least one container that requires zero CPUs.
			// See the comments in policy_static.go for more details.
			return nil, 0, fmt.Errorf("[cpumanager] the static policy requires systemreserved.cpu + kubereserved.cpu to be greater than zero")
		}

		// Take the ceiling of the reservation, since fractional CPUs cannot be
		// exclusively allocated.
		reservedCPUsFloat := float64(reservedCPUs.MilliValue()) / 1000
		numReservedCPUs := int(math.Ceil(reservedCPUsFloat))
		// If cpu reserved is enabled, the reserved cores must be integer, or will lead to wrong state for pods with non-integer cpu requests
		if cpuReservedEnabled {
			if reservedCPUsFloat != float64(numReservedCPUs) {
				errMsg := fmt.Sprintf("[cpumanager] static policy with cpu reserved enabled, reserved cores must be integer, but is %f\n", reservedCPUsFloat)
				klog.Error(errMsg)
				panic(errMsg)
			}
		}
		return topo, numReservedCPUs, nil
	}

	// Record reserved cpu sets
	reservedCpus := cpuset.NewCPUSet()

	switch policyName(cpuPolicyName) {
	case PolicyNone:
		policy = NewNonePolicy()
	case PolicyStatic:
		topo, numReservedCPUs, err := topoFunc()
		if err != nil {
			return nil, err
		}
		policy = NewStaticPolicy(topo, numReservedCPUs)
		if cpuReservedEnabled {
			// Record reserved cpu sets
			reservedCpus = policy.(*staticPolicy).reserved
		}
	case PolicyAffiliate:
		topo, numReservedCPUs, err := topoFunc()
		if err != nil {
			return nil, err
		}
		policy = NewAffiliatePolicy(topo, numReservedCPUs)
	default:
		klog.Errorf("[cpumanager] Unknown policy \"%s\", falling back to default policy \"%s\"", cpuPolicyName, PolicyNone)
		policy = NewNonePolicy()
	}

	stateImpl, err := state.NewCheckpointState(stateFileDirectory, cpuManagerStateFileName, policy.Name(), reservedCpus)
	if err != nil {
		return nil, fmt.Errorf("could not initialize checkpoint manager: %v", err)
	}

	manager := &manager{
		policy:                     policy,
		reconcilePeriod:            reconcilePeriod,
		state:                      stateImpl,
		machineInfo:                machineInfo,
		nodeAllocatableReservation: nodeAllocatableReservation,
	}
	manager.sourcesReady = &sourcesReadyStub{}
	return manager, nil
}

func (m *manager) Start(activePods ActivePodsFunc, sourcesReady config.SourcesReady, podStatusProvider status.PodStatusProvider, containerRuntime runtimeService) {
	klog.Infof("[cpumanager] starting with %s policy", m.policy.Name())
	klog.Infof("[cpumanager] reconciling every %v", m.reconcilePeriod)
	m.sourcesReady = sourcesReady
	m.activePods = activePods
	m.podStatusProvider = podStatusProvider
	m.containerRuntime = containerRuntime

	m.policy.Start(m.state, podStatusProvider)
	if m.policy.Name() == string(PolicyNone) {
		return
	}
	go wait.Until(func() { m.reconcileState() }, m.reconcilePeriod, wait.NeverStop)
}

func (m *manager) AddContainer(p *v1.Pod, c *v1.Container, containerID string) error {
	m.Lock()
	err := m.policy.AddContainer(m.state, p, c, containerID)
	if err != nil {
		klog.Errorf("[cpumanager] AddContainer error: %v", err)
		m.Unlock()
		return err
	}
	cpus := m.state.GetCPUSetOrDefault(containerID)
	m.Unlock()

	if !cpus.IsEmpty() {
		err = m.updateContainerCPUSet(containerID, cpus)
		if err != nil {
			klog.Errorf("[cpumanager] AddContainer error: %v", err)
			m.Lock()
			err := m.policy.RemoveContainer(m.state, containerID)
			if err != nil {
				klog.Errorf("[cpumanager] AddContainer rollback state error: %v", err)
			}
			m.Unlock()
		}
		return err
	}
	klog.V(5).Infof("[cpumanager] update container resources is skipped due to cpu set is empty")
	return nil
}

func (m *manager) UpdateContainer(p *v1.Pod, c *v1.Container, containerID string) error {
	cpus, err := func() (cpuset.CPUSet, error) {
		m.Lock()
		defer m.Unlock()
		if err := m.policy.RemoveContainer(m.state, containerID); err != nil {
			klog.Errorf("[cpumanager] UpdateContainer error: %v", err)
			return cpuset.NewCPUSet(), err
		}
		if err := m.policy.AddContainer(m.state, p, c, containerID); err != nil {
			klog.Errorf("[cpumanager] UpdateContainer error: %v", err)
			return cpuset.NewCPUSet(), err
		}
		return m.state.GetCPUSetOrDefault(containerID), nil
	}()
	if err != nil {
		return err
	}

	if err := m.updateContainerCPUSet(containerID, cpus); err != nil {
		klog.Errorf("[cpumanager] UpdateContainer error: %v", err)
		return err
	}
	return nil
}

func (m *manager) RemoveContainer(containerID string) error {
	m.Lock()
	defer m.Unlock()

	err := m.policy.RemoveContainer(m.state, containerID)
	if err != nil {
		klog.Errorf("[cpumanager] RemoveContainer error: %v", err)
		return err
	}
	return nil
}

func (m *manager) State() state.Reader {
	return m.state
}

type reconciledContainer struct {
	podName       string
	containerName string
	containerID   string
}

func (m *manager) reconcileState() (success []reconciledContainer, failure []reconciledContainer) {
	if !m.sourcesReady.AllReady() {
		return
	}
	success = []reconciledContainer{}
	failure = []reconciledContainer{}

	activeContainers := make(map[string]*v1.Pod)

	for _, pod := range m.activePods() {
		allContainers := pod.Spec.InitContainers
		allContainers = append(allContainers, pod.Spec.Containers...)
		for _, container := range allContainers {
			status, ok := m.podStatusProvider.GetPodStatus(pod.UID)
			if !ok {
				klog.Warningf("[cpumanager] reconcileState: skipping pod; status not found (pod: %s, container: %s)", pod.Name, container.Name)
				failure = append(failure, reconciledContainer{pod.Name, container.Name, ""})
				break
			}

			containerID, err := findContainerIDByName(&status, container.Name)
			if err != nil {
				klog.Warningf("[cpumanager] reconcileState: skipping container; ID not found in status (pod: %s, container: %s, error: %v)", pod.Name, container.Name, err)
				failure = append(failure, reconciledContainer{pod.Name, container.Name, ""})
				continue
			}

			// Check whether container is present in state, there may be 3 reasons why it's not present:
			// - policy does not want to track the container
			// - kubelet has just been restarted - and there is no previous state file
			// - container has been removed from state by RemoveContainer call (DeletionTimestamp is set)
			if _, ok := m.state.GetCPUSet(containerID); !ok {
				if status.Phase == v1.PodRunning && pod.DeletionTimestamp == nil {
					klog.V(4).Infof("[cpumanager] reconcileState: container is not present in state - trying to add (pod: %s, container: %s, container id: %s)", pod.Name, container.Name, containerID)
					err := m.AddContainer(pod, &container, containerID)
					if err != nil {
						klog.Errorf("[cpumanager] reconcileState: failed to add container (pod: %s, container: %s, container id: %s, error: %v)", pod.Name, container.Name, containerID, err)
						failure = append(failure, reconciledContainer{pod.Name, container.Name, containerID})
						continue
					}
				} else {
					// if DeletionTimestamp is set, pod has already been removed from state
					// skip the pod/container since it's not running and will be deleted soon
					continue
				}
			}

			activeContainers[containerID] = pod

			cset := m.state.GetCPUSetOrDefault(containerID)
			if cset.IsEmpty() {
				// NOTE: This should not happen outside of tests.
				klog.Infof("[cpumanager] reconcileState: skipping container; assigned cpuset is empty (pod: %s, container: %s)", pod.Name, container.Name)
				failure = append(failure, reconciledContainer{pod.Name, container.Name, containerID})
				continue
			}

			klog.V(4).Infof("[cpumanager] reconcileState: updating container (pod: %s, container: %s, container id: %s, cpuset: \"%v\")", pod.Name, container.Name, containerID, cset)
			err = m.updateContainerCPUSet(containerID, cset)
			if err != nil {
				klog.Errorf("[cpumanager] reconcileState: failed to update container (pod: %s, container: %s, container id: %s, cpuset: \"%v\", error: %v)", pod.Name, container.Name, containerID, cset, err)
				failure = append(failure, reconciledContainer{pod.Name, container.Name, containerID})
				continue
			}
			success = append(success, reconciledContainer{pod.Name, container.Name, containerID})
		}
	}

	for containerID := range m.state.GetCPUAssignments() {
		if pod, ok := activeContainers[containerID]; !ok {
			err := m.RemoveContainer(containerID)
			if err != nil {
				klog.Errorf("[cpumanager] reconcileState: failed to remove container (pod: %s, container id: %s, error: %v)", pod.Name, containerID, err)
				failure = append(failure, reconciledContainer{pod.Name, "", containerID})
			}
		}
	}
	return success, failure
}

func findContainerIDByName(status *v1.PodStatus, name string) (string, error) {
	for _, container := range status.ContainerStatuses {
		if container.Name == name && container.ContainerID != "" {
			cid := &kubecontainer.ContainerID{}
			err := cid.ParseString(container.ContainerID)
			if err != nil {
				return "", err
			}
			return cid.ID, nil
		}
	}
	return "", fmt.Errorf("unable to find ID for container with name %v in pod status (it may not be running)", name)
}

func (m *manager) updateContainerCPUSet(containerID string, cpus cpuset.CPUSet) error {
	// TODO: Consider adding a `ResourceConfigForContainer` helper in
	// helpers_linux.go similar to what exists for pods.
	// It would be better to pass the full container resources here instead of
	// this patch-like partial resources.
	return m.containerRuntime.UpdateContainerResources(
		containerID,
		&runtimeapi.LinuxContainerResources{
			CpusetCpus: cpus.String(),
		})
}
