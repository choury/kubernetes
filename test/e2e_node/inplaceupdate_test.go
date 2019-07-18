/*
Copyright 2019 The Kubernetes Authors.

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

package e2e_node

import (
	"fmt"
	"time"
	"strconv"
	"io/ioutil"
	"path"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	kubeletconfig "k8s.io/kubernetes/pkg/kubelet/apis/config"
	"k8s.io/kubernetes/pkg/kubelet/cm"
	"k8s.io/kubernetes/test/e2e/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// enableInPlaceUpdateInKubelet enables pod inplace update feature for kubelet with a sensible default test limit
func enableInPlaceUpdateInKubelet(f *framework.Framework) *kubeletconfig.KubeletConfiguration {
	oldCfg, err := getCurrentKubeletConfig()
	framework.ExpectNoError(err)
	newCfg := oldCfg.DeepCopy()
	if newCfg.FeatureGates == nil {
		newCfg.FeatureGates = make(map[string]bool)
		newCfg.FeatureGates["InPlaceResourcesUpdate"] = true
	}
	// Update the Kubelet configuration.
	framework.ExpectNoError(setKubeletConfiguration(f, newCfg))

	// Wait for the Kubelet to be ready.
	Eventually(func() bool {
		nodeList := framework.GetReadySchedulableNodesOrDie(f.ClientSet)
		return len(nodeList.Items) == 1
	}, time.Minute, time.Second).Should(BeTrue())

	return oldCfg
}

func readCgroupInt(path string) (int, error){
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(strings.Trim(string(data), " \n"))
	return value, err
}

func checkPodCgroup(pod *apiv1.Pod) error {
	podUID := string(pod.UID)
	By("get pod cgroup information")
	cgroupFsName := ""
	cgroupName := cm.NewCgroupName(cm.RootCgroupName, defaultNodeAllocatableCgroup, "pod"+podUID)
	if framework.TestContext.KubeletConfig.CgroupDriver == "systemd" {
		cgroupFsName = cgroupName.ToSystemd()
	} else {
		cgroupFsName = cgroupName.ToCgroupfs()
	}
	cgroupSubsystem, err := cm.GetCgroupSubsystems()
	Expect(err).NotTo(HaveOccurred())
	containerID := strings.TrimPrefix(pod.Status.ContainerStatuses[0].ContainerID, "docker://")

	By("checking if the expected cgroup settings were applied")
	cpuPeriod, err := readCgroupInt(path.Join(cgroupSubsystem.MountPoints["cpu"], cgroupFsName, containerID, "cpu.cfs_period_us"))
	Expect(err).NotTo(HaveOccurred())
	cpuQuota, err := readCgroupInt(path.Join(cgroupSubsystem.MountPoints["cpu"], cgroupFsName, containerID, "cpu.cfs_quota_us"))
	Expect(err).NotTo(HaveOccurred())
	cpuExpect := pod.Spec.Containers[0].Resources.Limits.Cpu().Value()
	if cpuQuota/cpuPeriod !=  int(cpuExpect) {
		return fmt.Errorf("cgroup cpu not equal: %v -> %v", cpuQuota/cpuPeriod, cpuExpect)
	}

	memQuota, err := readCgroupInt(path.Join(cgroupSubsystem.MountPoints["memory"], cgroupFsName, containerID, "memory.limit_in_bytes"))
	Expect(err).NotTo(HaveOccurred())
	memExpect := pod.Spec.Containers[0].Resources.Limits.Memory().Value()
	if memQuota != int(memExpect) {
		return fmt.Errorf("cgroup memory not equal: %v -> %v", memQuota, memExpect)
	}

	return nil
}

func runPodInPlaceUpdateTests(f *framework.Framework) {
	It("should set cgroup for Pod", func() {
		By("by creating a G pod")
		pod := f.PodClient().Create(&apiv1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod" + string(uuid.NewUUID()),
				Namespace: f.Namespace.Name,
			},
			Spec: apiv1.PodSpec{
				Containers: []apiv1.Container{
					{
						Image: imageutils.GetPauseImageName(),
						Name:  "container" + string(uuid.NewUUID()),
						Resources: apiv1.ResourceRequirements{
							Limits: apiv1.ResourceList{
								apiv1.ResourceName("cpu"):    resource.MustParse("1"),
								apiv1.ResourceName("memory"): resource.MustParse("100Mi"),
							},
						},
					},
				},
			},
		})
		err := f.WaitForPodReady(pod.Name)
		Expect(err).NotTo(HaveOccurred())
		pod, err = f.PodClient().Get(pod.Name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		err = checkPodCgroup(pod)
		Expect(err).NotTo(HaveOccurred())

		By("by update a G pod")
		f.PodClient().Update(pod.Name, func(pod *apiv1.Pod){
			resource := apiv1.ResourceList{
				apiv1.ResourceName("cpu"):    resource.MustParse("2"),
				apiv1.ResourceName("memory"): resource.MustParse("50Mi"),
			}
			pod.Spec.Containers[0].Resources.Limits = resource
			pod.Spec.Containers[0].Resources.Requests = resource
		})

		_, err = f.PodClient().WaitForErrorEventOrSuccess(pod)
		Expect(err).NotTo(HaveOccurred())
		pod, err = f.PodClient().Get(pod.Name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		err = checkPodCgroup(pod)
		Expect(err).NotTo(HaveOccurred())
	})
}

// Serial because the test updates kubelet configuration.
var _ = SIGDescribe("PodInPlaceUpdate [Serial] [Feature:SupportPodInPlaceUpdate][NodeFeature:SupportPodInPlaceUpdate]", func() {
	f := framework.NewDefaultFramework("pod-inplace-update")
	Context("With config updated with inplace update feature enabled", func() {
		tempSetCurrentKubeletConfig(f, func(initialConfig *kubeletconfig.KubeletConfiguration) {
			if initialConfig.FeatureGates == nil {
				initialConfig.FeatureGates = make(map[string]bool)
			}
			initialConfig.FeatureGates["InPlaceResourcesUpdate"] = true
		})
		runPodInPlaceUpdateTests(f)
	})
})
