/*
Copyright 2014 The Kubernetes Authors All rights reserved.

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

package qcloud

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog"
	cbsv3 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs/v20170312"
	cbscommon "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"k8s.io/apimachinery/pkg/types"
)

const (
	errorVolumeNotFound = "InvalidVolume.DisksNotFound"
)

var (
	StorageTypeCloudBasic   = "CLOUD_BASIC"
	StorageTypeCloudPremium = "CLOUD_PREMIUM"
	StorageTypeCloudSSD     = "CLOUD_SSD"
	prePay                  = "PREPAID"
	postPay                 = "POSTPAID_BY_HOUR"
	TaskCheckInterval       = time.Second * 1
	StatusUnattached = "UNATTACHED"
	StatusAttached   = "ATTACHED"

)

type ErrVolumeNotFound struct {
	diskIds []string
}

func (e ErrVolumeNotFound) Code() string {
	return errorVolumeNotFound
}

func (e ErrVolumeNotFound) Error() string {
	return fmt.Sprintf("disks %v not found", e.diskIds)
}

func (e ErrVolumeNotFound) OrignalErr() error {
	return nil
}

type DisksV3 interface {
	// AttachDisk attaches given disk to given node. Current node
	// is used when nodeName is empty string.
	AttachDiskV3(diskId string, nodename types.NodeName) error

	// DetachDisk detaches given disk to given node. Current node
	// is used when nodeName is empty string.
	// Assumption: If node doesn't exist, disk is already detached from node.
	DetachDiskV3(diskId string, nodename types.NodeName) error

	// DiskIsAttached checks if a disk is attached to the given node.
	// Assumption: If node doesn't exist, disk is not attached to the node.
	DiskIsAttachedV3(diskId string, nodename types.NodeName) (bool, error)

	// DisksAreAttached checks if a list disks are attached to the given node.
	// Assumption: If node doesn't exist, disks are not attached to the node.
	DisksAreAttachedV3(diskIds []string, nodename types.NodeName) (map[string]bool, error)

	// CreateDisk creates a new cbs disk with specified parameters.
	CreateDiskV3(diskType string, sizeGb int, zone string, paymode string) (string, int, error)

	// DeleteVolume deletes cbs disk.
	DeleteDiskV3(diskId string) error

	BindDiskToAspV3(diskId string, aspId string) error
}

func (qcloud *QCloud) CreateDiskV3(diskType string, sizeGb int, zone string, paymode string) (string, int, error) {
	klog.Infof("CreateDisk, diskType:%s, size:%d, zone:%s",
		diskType, sizeGb, zone)

	createType := ""
	var size int
	var createPaymode string
	switch paymode {
	case prePay, postPay:
		createPaymode = paymode
	default:
		createPaymode = postPay
	}
	switch diskType {
	case StorageTypeCloudBasic, StorageTypeCloudPremium, StorageTypeCloudSSD:
		createType = diskType
	default:
		//通过查询cbs available情况智能选择cbs类型
		request := cbsv3.NewDescribeDiskConfigQuotaRequest()
		request.InquiryType = cbscommon.StringPtr("INQUIRY_CBS_CONFIG")
		request.Zones = cbscommon.StringPtrs([]string{zone})
		request.DiskChargeType = cbscommon.StringPtr(createPaymode)
		request.DiskTypes = cbscommon.StringPtrs([]string{StorageTypeCloudBasic, StorageTypeCloudPremium})
		resp, err := qcloud.cbsV3.DescribeDiskConfigQuota(request)
		if err != nil {
			return "", 0, err
		}
		for _, diskConfig := range resp.Response.DiskConfigSet {
			if *diskConfig.Available == true && *diskConfig.DiskType == StorageTypeCloudPremium {
				createType = StorageTypeCloudPremium
			}
			if *diskConfig.Available == true && *diskConfig.DiskType == StorageTypeCloudBasic {
				createType = StorageTypeCloudBasic
				break
			}
		}
	}
	if createType == "" {
		return "", 0, fmt.Errorf("no available storage in zone: %s", zone)
	}

	if sizeGb == sizeGb/10*10 {
		size = sizeGb
	} else {
		// size = ((sizeGb / 10) + 1) * 10
                // 不进行向上取整，直接报错
		return "", 0, fmt.Errorf("cbs size is must be a integer multiple of 10G, %v", sizeGb)
	}

	if size > 16000 {
		size = 16000
	}

	if createType == StorageTypeCloudSSD && size < 100 {
		size = 100
	}
	request := cbsv3.NewCreateDisksRequest()
	request.DiskType = cbscommon.StringPtr(createType)
	request.DiskChargeType = cbscommon.StringPtr(createPaymode)
	request.DiskSize = cbscommon.Uint64Ptr(uint64(size))
	request.Placement = &cbsv3.Placement{Zone: cbscommon.StringPtr(zone)}
	if createPaymode == prePay {
		request.DiskChargePrepaid = &cbsv3.DiskChargePrepaid{Period: cbscommon.Uint64Ptr(1)}
	}
	resp, err := qcloud.cbsV3.CreateDisks(request)
	if err != nil {
		return "", 0, err
	}
	storageIds := resp.Response.DiskIdSet
	if len(storageIds) != 1 {
		return "", 0, fmt.Errorf("err:len(storageIds)=%d", len(storageIds))
	}
	//wait until cbs creating finished
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*180)
	defer cancel()
	ticker := time.NewTicker(TaskCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", 0, fmt.Errorf("wait 180 seconds and disk %v status is not UNATTACHED yet", *(storageIds[0]))
		case <-ticker.C:
			request := cbsv3.NewDescribeDisksRequest()
			request.DiskIds = storageIds
			response, err := qcloud.cbsV3.DescribeDisks(request)
			if err != nil {
				return "", 0, err
			}
			if *response.Response.TotalCount == 1 && response.Response.DiskSet[0].DiskState != nil {
				if *response.Response.DiskSet[0].DiskState == "UNATTACHED" {
					return *storageIds[0], size, nil
				}
			}
		}
	}
}

func (qcloud *QCloud) DeleteDiskV3(diskId string) error {
	request := cbsv3.NewTerminateDisksRequest()
	request.DiskIds = cbscommon.StringPtrs([]string{diskId})
	_, err := qcloud.cbsV3.TerminateDisks(request)
	return err
}

func (qcloud *QCloud) AttachDiskV3(diskId string, nodename types.NodeName) error {
 	nodeName := string(nodename)

	klog.Infof("AttachDisk, diskId:%s, node:%s", diskId, nodeName)

	instance, err := qcloud.getInstanceInfoByNodeNameV3(nodeName)
	if err != nil {
		return err
	}

	listCbsRequest := cbsv3.NewDescribeDisksRequest()
	listCbsRequest.DiskIds = cbscommon.StringPtrs([]string{diskId})

	listCbsResponse, err :=  qcloud.cbsV3.DescribeDisks(listCbsRequest)
	if err != nil {
		return err
	}

	if len(listCbsResponse.Response.DiskSet) <= 0 {
		return ErrVolumeNotFound{diskIds: []string{diskId}}
	}

	for _, disk := range listCbsResponse.Response.DiskSet {
		if *disk.DiskId == diskId {
			// disk has already attached
			if *disk.DiskState == StatusAttached && *disk.InstanceId == *instance.InstanceId {
				return nil
			}
			if *disk.DiskState == StatusAttached && *disk.InstanceId != *instance.InstanceId {
				return fmt.Errorf("disk is attach to another instance already")
			}
		}
	}

	attachDiskRequest := cbsv3.NewAttachDisksRequest()
	attachDiskRequest.DiskIds = cbscommon.StringPtrs([]string{diskId})
	attachDiskRequest.InstanceId = instance.InstanceId

	_, err = qcloud.cbsV3.AttachDisks(attachDiskRequest)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(time.Second * 3)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*120)
	defer cancel()

	for {
		select {
		case <-ticker.C:
			listCbsRequest := cbsv3.NewDescribeDisksRequest()
			listCbsRequest.DiskIds = cbscommon.StringPtrs([]string{diskId})

			listCbsResponse, err := qcloud.cbsV3.DescribeDisks(listCbsRequest)
			if err != nil {
				continue
			}
			if len(listCbsResponse.Response.DiskSet) >= 1 {
				for _, d := range listCbsResponse.Response.DiskSet {
					if *d.DiskId == diskId && d.DiskState != nil {
						if *d.DiskState == StatusAttached {
							return nil
						}
					}
				}
			}
		case <-ctx.Done():
			return fmt.Errorf("cbs disk %v is not attached before deadline exceeded", diskId)
		}
	}
}

func (qcloud *QCloud) DetachDiskV3(diskId string, hostName types.NodeName) error {
	attached, err := qcloud.DiskIsAttachedV3(diskId, hostName)
	if err != nil {
		if isQcloudErrorVolumeNotFound(err) {
			klog.Warningf(
				"DetachDisk %s is called for node %s, but cbs disk does not exist; assuming the disk is detached",
				diskId, hostName)

			return nil
		}

		return err
	}

	if !attached {
		// Volume is not attached to node. Success!
		klog.Infof("Detach operation is successful. cbs disk(%s) was not attached to node(%s)", diskId, hostName)
		return nil
	}

	detachDiskRequest := cbsv3.NewDetachDisksRequest()
	detachDiskRequest.DiskIds = cbscommon.StringPtrs([]string{diskId})

	_, err = qcloud.cbsV3.DetachDisks(detachDiskRequest)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(time.Second * 3)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*120)
	defer cancel()

	for {
		select {
		case <-ticker.C:
			listCbsRequest := cbsv3.NewDescribeDisksRequest()
			listCbsRequest.DiskIds = cbscommon.StringPtrs([]string{diskId})

			listCbsResponse, err := qcloud.cbsV3.DescribeDisks(listCbsRequest)
			if err != nil {
				continue
			}
			if len(listCbsResponse.Response.DiskSet) >= 1 {
				for _, d := range listCbsResponse.Response.DiskSet {
					if *d.DiskId == diskId && d.DiskState != nil {
						if *d.DiskState == StatusUnattached {
							return nil
						}
					}
				}
			}
		case <-ctx.Done():
			return fmt.Errorf("cbs disk  %v is not unattached before deadline exceeded", diskId)
		}
	}

}

func (qcloud *QCloud) DiskIsAttachedV3(diskId string, nodename types.NodeName) (bool, error) {
	nodeName := string(nodename)

	klog.Infof("DiskIsAttached, diskId:%s, nodeName:%s", diskId, nodeName)

	instance, err := qcloud.getInstanceInfoByNodeNameV3(nodeName)
	if err != nil {
		if err == QcloudInstanceNotFound {
			// If instance no longer exists, safe to assume volume is not attached.
			klog.Warningf(
				"Instance %q does not exist. DiskIsAttached will assume disk %q is not attached to it.",
				nodeName,
				diskId)
			return false, nil
		}
		return false, fmt.Errorf("Check DiskIsAttached failed, disk:%s, get instance:%s, error:%v", diskId, nodeName, err)
	}

	listCbsRequest := cbsv3.NewDescribeDisksRequest()
	listCbsRequest.DiskIds = cbscommon.StringPtrs([]string{diskId})

	listCbsResponse, err := qcloud.cbsV3.DescribeDisks(listCbsRequest)
	if err != nil {
		return false, err
	}

	if len(listCbsResponse.Response.DiskSet) <= 0 {
		return false, ErrVolumeNotFound{diskIds: []string{diskId}}
	}

	for _, disk := range listCbsResponse.Response.DiskSet {
		if diskId == *disk.DiskId {
			if *disk.Attached && *disk.InstanceId == *instance.InstanceId {
				return true, nil
			}
		}
	}
	return false, nil
}

func (qcloud *QCloud) DisksAreAttachedV3(diskIds []string, nodename types.NodeName) (map[string]bool, error) {
	nodeName := string(nodename)

	attached := make(map[string]bool)

	for _, diskId := range diskIds {
		attached[diskId] = false
	}

	instance, err := qcloud.getInstanceInfoByNodeNameV3(nodeName)
	if err != nil {
		if err == QcloudInstanceNotFound {
			klog.Warningf("DiskAreAttached, node is not found, assume disks are not attched to the node:%s",
				nodeName)
			return attached, nil
		}
		return attached, fmt.Errorf("Check DisksAreAttached, get node info failed, disks:%v, get instance:%s, error:%v",
			diskIds, nodeName, err)
	}

	listCbsRequest := cbsv3.NewDescribeDisksRequest()
	listCbsRequest.DiskIds = cbscommon.StringPtrs(diskIds)

	listCbsResponse, err := qcloud.cbsV3.DescribeDisks(listCbsRequest)
	if err != nil {
		return nil, err
	}

	if len(listCbsResponse.Response.DiskSet) <= 0 {
		return nil, ErrVolumeNotFound{diskIds: diskIds}
	}

	for _, disk := range listCbsResponse.Response.DiskSet {
		if *disk.Attached && *disk.InstanceId == *instance.InstanceId {
			attached[*disk.DiskId] = true
		}
	}

	return attached, nil
}

func (qcloud *QCloud) ModifyDiskNameV3(diskId string, name string) error {
	request := cbsv3.NewModifyDiskAttributesRequest()
	request.DiskIds = cbscommon.StringPtrs([]string{diskId})
	request.DiskName = cbscommon.StringPtr(name)
	_, err := qcloud.cbsV3.ModifyDiskAttributes(request)
	return err
}

func (qcloud *QCloud) BindDiskToAspV3(diskId string, aspId string) error {
	bindAspRequest := cbsv3.NewBindAutoSnapshotPolicyRequest()
	bindAspRequest.AutoSnapshotPolicyId = &aspId
	bindAspRequest.DiskIds = cbscommon.StringPtrs([]string{diskId})

	_, err := qcloud.cbsV3.BindAutoSnapshotPolicy(bindAspRequest)
	return err
}

func isQcloudErrorVolumeNotFound(err error) bool {
	if err != nil {
		if qcErr, ok := err.(QcloudError); ok {
			if qcErr.Code() == errorVolumeNotFound {
				return true
			}
		}
	}
	return false
}