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
	"fmt"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	qcloud "cloud.tencent.com/tencent-cloudprovider/provider"
	"k8s.io/api/core/v1"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/kubernetes/pkg/volume"

	volumehelper "k8s.io/kubernetes/pkg/volume/util"
)

type qcloudCbsAttacher struct {
	host        volume.VolumeHost
	qcloudDisks qcloud.DisksV3
}

var _ volume.Attacher = &qcloudCbsAttacher{}
var _ volume.AttachableVolumePlugin = &qcloudDiskPlugin{}

func (plugin *qcloudDiskPlugin) CanAttach(spec *volume.Spec) bool {
	return true
}

func (plugin *qcloudDiskPlugin) NewAttacher() (volume.Attacher, error) {
	qCloud, err := getCloudProvider(plugin.host.GetCloudProvider())
	if err != nil {
		return nil, err
	}

	return &qcloudCbsAttacher{
		host:        plugin.host,
		qcloudDisks: qCloud,
	}, nil
}

func (plugin *qcloudDiskPlugin) NewDeviceMounter() (volume.DeviceMounter, error) {
	return plugin.NewAttacher()
}

func (attacher *qcloudCbsAttacher) Attach(spec *volume.Spec, hostname types.NodeName) (string, error) {
	hostName := string(hostname)
	volumeSource, _, err := getVolumeSource(spec)
	if err != nil {
		return "", err
	}
	diskId := volumeSource.CbsDiskId

	klog.V(4).Infof("Attach disk called for host %s", hostName)

	attached, err := attacher.qcloudDisks.DiskIsAttachedV3(diskId, hostname)
	if err != nil {
		klog.Errorf("check cbs disk(%q) is attached to node(%q) error(%v)",
			diskId, hostName, err)

		return "", err
	}

	if attached {
		klog.Infof("cbs(%q) is already attached to node(%q), attach return success.", diskId, hostName)
	} else {
		if err := attacher.qcloudDisks.AttachDiskV3(diskId, hostname); err != nil {
			klog.Errorf("error attaching cbs(%s) to node(%s): %+v", diskId, hostName, err)
			return "", err
		}
	}

	//TODO
	return path.Join(diskByIDPath, diskQCloudPrefix+diskId), nil
}

func (attacher *qcloudCbsAttacher) VolumesAreAttached(specs []*volume.Spec, nodename types.NodeName) (map[*volume.Spec]bool, error) {
	nodeName := string(nodename)

	volumesAttachedCheck := make(map[*volume.Spec]bool)
	volumeDiskIdMap := make(map[string]*volume.Spec)
	diskIdList := []string{}
	for _, spec := range specs {
		volumeSource, _, err := getVolumeSource(spec)
		// If error is occured, skip this volume and move to the next one
		if err != nil {
			klog.Errorf("Error getting volume (%q) source : %v", spec.Name(), err)
			continue
		}
		diskIdList = append(diskIdList, volumeSource.CbsDiskId)
		volumesAttachedCheck[spec] = true
		volumeDiskIdMap[volumeSource.CbsDiskId] = spec
	}
	attachedResult, err := attacher.qcloudDisks.DisksAreAttachedV3(diskIdList, nodename)
	if err != nil {
		// Log error and continue with attach
		klog.Errorf(
			"check cbsDisks are attached(%v) to current node(%q) error: %v",
			diskIdList, nodeName, err)
		return volumesAttachedCheck, err
	}

	for diskId, attached := range attachedResult {
		if !attached {
			spec := volumeDiskIdMap[diskId]
			volumesAttachedCheck[spec] = false
			klog.V(2).Infof("VolumesAreAttached: check volume %q (specName: %q) is no longer attached",
				diskId, spec.Name())
		}
	}
	return volumesAttachedCheck, nil
}

func (attacher *qcloudCbsAttacher) WaitForAttach(spec *volume.Spec, devicePath string, _ *v1.Pod, timeout time.Duration) (string, error) {

	volumeSource, _, err := getVolumeSource(spec)
	if err != nil {
		return "", err
	}

	if devicePath == "" {
		return "", fmt.Errorf("WaitForAttach failed, devicePath is empty, cbs disk(%s)", volumeSource.CbsDiskId)
	}

	ticker := time.NewTicker(checkSleepDuration)
	defer ticker.Stop()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ticker.C:
			klog.V(5).Infof("Checking cbs disk is attached", volumeSource.CbsDiskId)
			// TODO because udev or tlinux bug, we maybe traverse /sys/block/vdx/serial to find disk
			path, err := verifyDevicePath(devicePath, volumeSource.CbsDiskId)
			if err != nil {
				// Log error, if any, and continue checking periodically. See issue #11321
				klog.Warningf("Error verifying disk (%q) is attached: %v", volumeSource.CbsDiskId, err)
			} else if path != "" {
				// A device path has successfully been created
				klog.Infof("Successfully found attached disk(%q)", volumeSource.CbsDiskId)
				return path, nil
			}
		case <-timer.C:
			return "", fmt.Errorf("Could not find attached disk(%q). Timeout waiting for mount paths to be created.",
				volumeSource.CbsDiskId)
		}
	}
}

// GetDeviceMountPath returns a path where the device should
// point which should be bind mounted for individual volumes.
func (attacher *qcloudCbsAttacher) GetDeviceMountPath(spec *volume.Spec) (string, error) {
	volumeSource, _, err := getVolumeSource(spec)
	if err != nil {
		return "", err
	}

	return makeGlobalPDPath(attacher.host, volumeSource.CbsDiskId), nil
}

// GetMountDeviceRefs finds all other references to the device referenced
// by deviceMountPath; returns a list of paths.
func (plugin *qcloudDiskPlugin) GetDeviceMountRefs(deviceMountPath string) ([]string, error) {
	mounter := plugin.host.GetMounter(qcloudCbsPluginName)
	return mounter.GetMountRefs(deviceMountPath)
}

// MountDevice mounts device to global mount point.
func (attacher *qcloudCbsAttacher) MountDevice(spec *volume.Spec, devicePath string, deviceMountPath string) error {
	mounter := attacher.host.GetMounter(qcloudCbsPluginName)
	notMnt, err := mounter.IsLikelyNotMountPoint(deviceMountPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(deviceMountPath, 0750); err != nil {
				return err
			}
			notMnt = true
		} else {
			return err
		}
	}

	volumeSource, readOnly, err := getVolumeSource(spec)
	if err != nil {
		return err
	}

	options := []string{}
	if readOnly {
		options = append(options, "ro")
	}
	if notMnt {
		diskMounter := volumehelper.NewSafeFormatAndMountFromHost(qcloudCbsPluginName, attacher.host)
		err = diskMounter.FormatAndMount(devicePath, deviceMountPath, volumeSource.FSType, options)
		if err != nil {
			os.Remove(deviceMountPath)
			return err
		}
		klog.V(4).Infof("formatting spec %v devicePath %v deviceMountPath %v fs %v with options %+v", spec.Name(), devicePath, deviceMountPath, volumeSource.FSType, options)
	}
	return nil
}

type qcloudCbsDetacher struct {
	mounter    mount.Interface
	qcloudDisk qcloud.DisksV3
}

var _ volume.Detacher = &qcloudCbsDetacher{}

func (plugin *qcloudDiskPlugin) NewDetacher() (volume.Detacher, error) {
	qcloud, err := getCloudProvider(plugin.host.GetCloudProvider())
	if err != nil {
		return nil, err
	}

	return &qcloudCbsDetacher{
		mounter:    plugin.host.GetMounter(qcloudCbsPluginName),
		qcloudDisk: qcloud,
	}, nil
}

func (plugin *qcloudDiskPlugin) NewDeviceUnmounter() (volume.DeviceUnmounter, error) {
	return plugin.NewDetacher()
}

// Detach the given device from the given host.
func (detacher *qcloudCbsDetacher) Detach(deviceMountPath string, hostname types.NodeName) error {
	hostName := hostname

	klog.Infof("Detach cbs disk from node(%s), mount path: %s", hostName, deviceMountPath)
	//TODO
	diskId := path.Base(deviceMountPath)

	if err := detacher.qcloudDisk.DetachDiskV3(diskId, hostName); err != nil {
		klog.Errorf("Error detaching cbs disk(%q) from node(%q): %v", diskId, hostName, err)
		return err
	}

	return nil
}

func (detacher *qcloudCbsDetacher) WaitForDetach(devicePath string, timeout time.Duration) error {
	ticker := time.NewTicker(checkSleepDuration)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ticker.C:
			klog.V(5).Infof("Checking device %q is detached.", devicePath)
			// TODO udev bug maybe need find /sys/block/vdx/serial
			if pathExists, err := mount.PathExists(devicePath); err != nil {
				return fmt.Errorf("Error checking if device path exists: %v", err)
			} else if !pathExists {
				return nil
			}
		case <-timer.C:
			return fmt.Errorf("Timeout reached; cbs disk path %v is still attached", devicePath)
		}
	}
}

func (detacher *qcloudCbsDetacher) UnmountDevice(deviceMountPath string) error {
	return mount.CleanupMountPoint(deviceMountPath, detacher.mounter, false)
}

func getDevicePathsBySerial(diskId string) (string, error) {
	dirs, _ := filepath.Glob("/sys/block/*")
	for _, dir := range dirs {
		serialPath := filepath.Join(dir, "serial")
		serialPathExist, err := pathExist(serialPath)
		if err != nil {
			return "", err
		}

		if serialPathExist {
			content, err := ioutil.ReadFile(serialPath)
			if err != nil {
				klog.Errorf("Failed to get diskId from serial path(%s): %v", serialPath, err)
				return "", err
			}

			if string(content) == diskId {
				arr := strings.Split(dir, "/")
				return filepath.Join("/dev/", arr[len(arr)-1]), nil
			}
		}
	}

	klog.Errorf("can not find diskId %v by serial", diskId)
	return "", fmt.Errorf("can not find diskId %v by serial", diskId)
}

func pathExist(path string) (bool, error) {
	_, err := os.Stat(path)

	if err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}
