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

package qcloud_cbs_dirty

import (
	"fmt"
	"os"
	"path"

	"github.com/golang/glog"
	"k8s.io/api/core/v1"


	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/util/mount"
	utilstrings "k8s.io/kubernetes/pkg/util/strings"
	"k8s.io/kubernetes/pkg/volume"
	"strings"
	volumehelper "k8s.io/kubernetes/pkg/volume/util"
)

// This is the primary entrypoint for volume plugins.
func ProbeVolumePlugins() []volume.VolumePlugin {
	return []volume.VolumePlugin{&qcloudDiskPlugin{}}
}

type qcloudDiskPlugin struct {
	host volume.VolumeHost
}

var _ volume.VolumePlugin = &qcloudDiskPlugin{}

const (
	qcloudCbsPluginName = "cloud.tencent.com/qcloud-cbs"
)

func (plugin *qcloudDiskPlugin) Init(host volume.VolumeHost) error {
	plugin.host = host
	return nil
}

func (plugin *qcloudDiskPlugin) GetPluginName() string {
	return qcloudCbsPluginName
}

func (plugin *qcloudDiskPlugin) GetVolumeName(spec *volume.Spec) (string, error) {
	volumeSource, _, err := getVolumeSource(spec)
	if err != nil {
		return "", err
	}

	return volumeSource.CbsDiskId, nil
}

func (plugin *qcloudDiskPlugin) CanSupport(spec *volume.Spec) bool {
	return false
}

func (plugin *qcloudDiskPlugin) RequiresRemount() bool {
	return false
}

func (plugin *qcloudDiskPlugin) SupportsBulkVolumeVerification() bool {
	return false
}

func (plugin *qcloudDiskPlugin) SupportsMountOption() bool {
	return true
}

func (plugin *qcloudDiskPlugin) NewMounter(spec *volume.Spec, pod *v1.Pod, _ volume.VolumeOptions) (volume.Mounter, error) {
	return plugin.newMounterInternal(spec, pod.UID, plugin.host.GetMounter(qcloudCbsPluginName))
}

func (plugin *qcloudDiskPlugin) NewUnmounter(volName string, podUID types.UID) (volume.Unmounter, error) {
	return plugin.newUnmounterInternal(volName, podUID, plugin.host.GetMounter(qcloudCbsPluginName))
}

func (plugin *qcloudDiskPlugin) newMounterInternal(spec *volume.Spec, podUID types.UID, mounter mount.Interface) (volume.Mounter, error) {
	qc, _, err := getVolumeSource(spec)
	if err != nil {
		return nil, err
	}

	diskId := qc.CbsDiskId
	fsType := qc.FSType
	if fsType == "" {
		fsType = "ext4"
	}

	return &qcloudCbsMounter{
		qcloudCbs: &qcloudCbs{
			podUID:  podUID,
			volName: spec.Name(),
			diskID:  diskId,
			mounter: mounter,
			plugin:  plugin,
		},
		fsType:      fsType,
		diskMounter: volumehelper.NewSafeFormatAndMountFromHost(plugin.GetPluginName(), plugin.host)}, nil
}

func (plugin *qcloudDiskPlugin) newUnmounterInternal(volName string, podUID types.UID, mounter mount.Interface) (volume.Unmounter, error) {
	return &qcloudCbsUnmounter{
		&qcloudCbs{
			podUID:  podUID,
			volName: volName,
			mounter: mounter,
			plugin:  plugin,
		}}, nil
}

func (plugin *qcloudDiskPlugin) ConstructVolumeSpec(volumeName, mountPath string) (*volume.Spec, error) {
	mounter := plugin.host.GetMounter(qcloudCbsPluginName)
	pluginDir := plugin.host.GetPluginDir(plugin.GetPluginName())
	sourceName, err := mounter.GetDeviceNameFromMount(mountPath, pluginDir)
	if err != nil {
		return nil, err
	}
	qcloudVolume := &v1.Volume{
		Name: volumeName,
		VolumeSource: v1.VolumeSource{
			QcloudCbs: &v1.QcloudCbsVolumeSource{
				CbsDiskId: sourceName,
			},
		},
	}
	return volume.NewSpecFromVolume(qcloudVolume), nil
}


// qcloudCbs volumes are disk resources are attached to the kubelet's host machine and exposed to the pod.
type qcloudCbs struct {
	volName     string
	podUID      types.UID
	// Unique identifier of the volume, used to find the disk resource in the provider.
	diskID      string
	// Filesystem type, optional.
	fsType      string
	// Utility interface that provides API calls to the provider to attach/detach disks.
	//manager     cbsManager
	// Mounter interface that provides system calls to mount the global path to the pod local path.
	mounter     mount.Interface
	// diskMounter provides the interface that is used to mount the actual block device.
	diskMounter mount.Interface
	plugin      *qcloudDiskPlugin
	volume.MetricsNil
}

var _ volume.Mounter = &qcloudCbsMounter{}

type qcloudCbsMounter struct {
	*qcloudCbs
	fsType      string
	diskMounter *mount.SafeFormatAndMount
	readOnly    bool
}

func (b *qcloudCbsMounter) GetAttributes() volume.Attributes {
	return volume.Attributes{
		SupportsSELinux: true,
	}
}

func (b *qcloudCbsMounter) CanMount() error {
	return nil
}

// SetUp attaches the disk and bind mounts to the volume path.
func (b *qcloudCbsMounter) SetUp(fsGroup *int64) error {
	return b.SetUpAt(b.GetPath(), fsGroup)
}

// SetUp attaches the disk and bind mounts to the volume path.
func (b *qcloudCbsMounter) SetUpAt(dir string, fsGroup *int64) error {

	notmnt, err := b.mounter.IsLikelyNotMountPoint(dir)
	glog.V(4).Infof("qcloud cbs SetUp check mount point, dir(%s), cbs disk(%s), error(%v), notmnt(%t)",
		dir, b.diskID, err, notmnt)

	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if !notmnt {
		return nil
	}

	if err := os.MkdirAll(dir, 0750); err != nil {
		glog.V(4).Infof("Could not create directory %s: %v", dir, err)
		return err
	}

	options := []string{"bind"}
	globalPDPath := makeGlobalPDPath(b.plugin.host, b.diskID)

	glog.V(4).Infof("attempting to mount %s to %s", globalPDPath, dir)
	err = b.mounter.Mount(globalPDPath, dir, "", options)

	if err != nil {
		notmnt, mntErr := b.mounter.IsLikelyNotMountPoint(dir)
		if mntErr != nil {
			glog.Errorf("IsLikelyNotMountPoint check failed: %v", mntErr)
			return err
		}
		if !notmnt {
			if mntErr = b.mounter.Unmount(dir); mntErr != nil {
				glog.Errorf("Failed to unmount: %v", mntErr)
				return err
			}
			notmnt, mntErr := b.mounter.IsLikelyNotMountPoint(dir)
			if mntErr != nil {
				glog.Errorf("IsLikelyNotMountPoint check failed: %v", mntErr)
				return err
			}
			if !notmnt {
				glog.Errorf("%s is still mounted, despite call to unmount().  Will try again next sync loop.", b.GetPath())
				return err
			}
		}

		glog.Infof("mount %s failed: %v", dir, err)
		os.Remove(dir)
		return err
	}

	if !b.readOnly {
		volume.SetVolumeOwnership(b, fsGroup)
	}

	glog.V(3).Infof("cbs volume %s mounted to %s succeded", b.diskID, dir)

	return nil
}

var _ volume.Unmounter = &qcloudCbsUnmounter{}

type qcloudCbsUnmounter struct {
	*qcloudCbs
}

// Unmounts the bind mount, and detaches the disk only if the PD
// resource was the last reference to that disk on the kubelet.
func (v *qcloudCbsUnmounter) TearDown() error {
	return v.TearDownAt(v.GetPath())
}

// Unmounts the bind mount, and detaches the disk only if the PD
// resource was the last reference to that disk on the kubelet.
func (v *qcloudCbsUnmounter) TearDownAt(dir string) error {
	glog.V(3).Infof("qcloud cbs TearDown of %s", dir)
	notMnt, err := v.mounter.IsLikelyNotMountPoint(dir)
	if err != nil {
		return err
	}
	if notMnt {
		return os.Remove(dir)
	}
	if err := v.mounter.Unmount(dir); err != nil {
		return err
	}
	notMnt, mntErr := v.mounter.IsLikelyNotMountPoint(dir)
	if mntErr != nil {
		glog.Errorf("IsLikelyNotMountPoint check failed: %v", mntErr)
		return err
	}
	if notMnt {
		return os.Remove(dir)
	}

	mounter := v.plugin.host.GetMounter(qcloudCbsPluginName)
	refs, err := mounter.GetMountRefs(dir)
	if err != nil {
		fmt.Errorf("Failed to unmount volume dir:%s,err:%v", dir, err)
	}
	for _, ref := range refs {
		if strings.Contains(ref, "cloud.tencent.com") {

			notMnt, err := v.mounter.IsLikelyNotMountPoint(ref)
			if err != nil {
				return err
			}
			if notMnt {
				return os.Remove(ref)
			}
			if err := v.mounter.Unmount(ref); err != nil {
				return err
			}
			notMnt, mntErr := v.mounter.IsLikelyNotMountPoint(ref)
			if mntErr != nil {
				glog.Errorf("IsLikelyNotMountPoint check failed: %v", mntErr)
				return err
			}
			if notMnt {
				return os.Remove(ref)
			}

		}
	}

	return fmt.Errorf("Failed to unmount volume dir:%s", dir)
}

func makeGlobalPDPath(host volume.VolumeHost, devName string) string {
	return path.Join(host.GetPluginDir(qcloudCbsPluginName), "mounts", devName)
}

func (vv *qcloudCbs) GetPath() string {
	return vv.plugin.host.GetPodVolumeDir(vv.podUID,
		utilstrings.EscapeQualifiedNameForDisk(qcloudCbsPluginName), vv.volName)
}

func getVolumeSource(
spec *volume.Spec) (*v1.QcloudCbsVolumeSource, bool, error) {
	if spec.Volume != nil && spec.Volume.QcloudCbs != nil {
		return spec.Volume.QcloudCbs, spec.ReadOnly, nil
	} else if spec.PersistentVolume != nil &&
		spec.PersistentVolume.Spec.QcloudCbs != nil {
		return spec.PersistentVolume.Spec.QcloudCbs, spec.ReadOnly, nil
	}

	return nil, false, fmt.Errorf("Spec does not reference a qcloudCbs type")
}
