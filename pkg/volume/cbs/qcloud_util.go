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

package qcloud_cbs

import (
	"errors"
	"os"
	"time"

	qcloud "cloud.tencent.com/tencent-cloudprovider/provider"
	//"k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/cloud-provider"
	"k8s.io/klog"
	//"k8s.io/kubernetes/pkg/volume"
)

const (
	maxRetries = 10
	checkSleepDuration = time.Second
	diskByIDPath = "/dev/disk/by-id/"
	diskQCloudPrefix = "virtio-"
)

var ErrProbeVolume = errors.New("Error scanning attached volumes")

type QcloudCbsUtil struct{}

func verifyDevicePath(devicePath, diskId string) (string, error) {
	devicePathExist, err := pathExist(devicePath)
	if err != nil {
		return "", err
	}

	if !devicePathExist {
		devicePathFromSerial, err := getDevicePathsBySerial(diskId)
		if err != nil {
			return "", err
		}

		if err := os.Symlink(devicePathFromSerial, devicePath); err != nil {
			klog.Errorf("Failed to link devicePathFromSerial(%s) and devicePathFromKubelet(%s): %v", devicePathFromSerial, devicePath, err)
			return "", err
		}
	}

	return devicePath, nil
}

//// CreateVolume creates a qcloud volume.
////qcloud_cbs.go:provision函数调用
////TODO choose zone,add zone label
//func (util *QcloudCbsUtil) CreateVolume(c *qcloudCbsProvisioner) (string, int, error) {
//	cloud, err := getCloudProvider(c.plugin.host.GetCloudProvider())
//	if err != nil {
//		return "", 0, err
//	}
//
//	capacity := c.options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
//	requestBytes := capacity.Value()
//
//	volSizeGB := int(volume.RoundUpSize(requestBytes, 1024 * 1024 * 1024))
//
//	diskType := c.options.Parameters["type"]
//	zone := c.options.Parameters["zone"]
//	if zone == "" {
//		glog.V(2).Infof("CreateVolume, region:%s, zone:%s, diskType:%s, diskSize:%d",
//			zone, diskType, volSizeGB)
//		return "", 0, errors.New("region or zone not found")
//	}
//
//	diskId, err := cloud.CreateDisk(diskType, volSizeGB, zone, *c.options.CloudTags)
//	if err != nil {
//		glog.V(2).Infof("Error creating qcloud cbs, size:%d, error: %v", volSizeGB, err)
//		return "", 0, err
//	}
//	glog.V(2).Infof("Successfully created qcloud cbs disk: %s", diskId)
//	return diskId, volSizeGB, nil
//}

//func (util *QcloudCbsUtil) DeleteVolume(deleter *qcloudCbsDeleter) error {
//	cloud, err := getCloudProvider(deleter.plugin.host.GetCloudProvider())
//	if err != nil {
//		return err
//	}
//
//	if err = cloud.DeleteDisk(deleter.diskID); err != nil {
//		glog.V(2).Infof("Error deleting cbs volume %s: %v", deleter.diskID, err)
//		return err
//	}
//	glog.V(2).Infof("Successfully deleted cbs volume %s", deleter.diskID)
//	return nil
//}

func getCloudProvider(cloud cloudprovider.Interface) (*qcloud.QCloud, error) {
	if cloud == nil {
		klog.Errorf("Cloud provider not initialized properly")
		return nil, errors.New("Cloud provider not initialized properly")
	}

	q := cloud.(*qcloud.QCloud)
	if q == nil {
		return nil, errors.New("Invalid cloud provider: expected QCloud")
	}
	return q, nil
}
