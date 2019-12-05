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
	"errors"
	"github.com/dbdd4us/qcloudapi-sdk-go/cvm"
	"k8s.io/api/core/v1"
	glog "k8s.io/klog"

	cloudprovider "k8s.io/cloud-provider"

	"k8s.io/apimachinery/pkg/types"

	"context"
	"fmt"
	"net/url"
	"strings"
)

func (self *QCloud) getInstanceInfoByNodeName(nodeName string) (*cvm.InstanceInfo, error) {
	if self.Config.NodeNameType != HostNameType {
		return self.getInstanceInfoByLanIp(nodeName)
	} else {
		instanceId, err := self.getInstanceIdByNodeName(nodeName)
		if err != nil {
			glog.Errorf("getInstanceIdByNodeName failed %s", err.Error())
			return nil, err
		}
		return self.getInstanceInfoById(instanceId)
	}

	return nil, QcloudInstanceNotFound
}

//TODO 隔离，已退还，退还中
func (self *QCloud) getInstanceInfoByLanIp(lanIP string) (*cvm.InstanceInfo, error) {
	filter := cvm.NewFilter(cvm.FilterNamePrivateIpAddress, lanIP)

	args := cvm.DescribeInstancesArgs{
		Version: cvm.DefaultVersion,
		Filters: &[]cvm.Filter{filter},
	}

	response, err := self.cvm.DescribeInstances(&args)
	if err != nil {
		return nil, err
	}
	instanceSet := response.InstanceSet
	for _, instance := range instanceSet {
		if instance.VirtualPrivateCloud.VpcID == self.Config.VpcId && stringIn(lanIP, instance.PrivateIPAddresses) {
			return &instance, nil
		}
	}
	return nil, QcloudInstanceNotFound
}

func (self *QCloud) getInstanceInfoById(instanceId string) (*cvm.InstanceInfo, error) {
	filter := cvm.NewFilter(cvm.FilterNameInstanceId, instanceId)

	args := cvm.DescribeInstancesArgs{
		Version: cvm.DefaultVersion,
		Filters: &[]cvm.Filter{filter},
	}

	response, err := self.cvm.DescribeInstances(&args)
	if err != nil {
		return nil, err
	}

	instanceSet := response.InstanceSet
	if len(instanceSet) == 0 {
		return nil, QcloudInstanceNotFound
	}
	if len(instanceSet) > 1 {
		return nil, fmt.Errorf("multiple instances found for instance: %s", instanceId)
	}
	return &instanceSet[0], nil
}

type kubernetesInstanceID string

// mapToInstanceID extracts the InstanceID from the kubernetesInstanceID
func (name kubernetesInstanceID) mapToInstanceID() (string, error) {
	s := string(name)

	if !strings.HasPrefix(s, "qcloud://") {
		// Assume a bare aws volume id (vol-1234...)
		// Build a URL with an empty host (AZ)
		s = "qcloud://" + "/" + "/" + s
	}

	u, err := url.Parse(s)
	if err != nil {
		glog.Errorf("Invalid instance name (%s): %v", name, err)
		return "", fmt.Errorf("Invalid instance name (%s): %v", name, err)
	}

	if u.Scheme != "qcloud" {
		glog.Errorf("Invalid scheme for Qcloud instance (%s)", name)
		return "", fmt.Errorf("Invalid scheme for Qcloud instance (%s)", name)
	}

	instanceId := ""
	tokens := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(tokens) == 1 {
		// instanceId
		instanceId = tokens[0]
	} else if len(tokens) == 2 {
		// az/instanceId
		instanceId = tokens[1]
	}

	if instanceId == "" || strings.Contains(instanceId, "/") || !strings.HasPrefix(instanceId, "ins-") {
		glog.Errorf("Invalid format for Qcloud instance (%s)", name)
		return "", fmt.Errorf("Invalid format for Qcloud instance (%s)", name)
	}

	return instanceId, nil
}

//TODO 如果NodeAddressesByProviderID失败，nodeController会调用此接口
func (self *QCloud) NodeAddresses(ctx context.Context, name types.NodeName) ([]v1.NodeAddress, error) {

	glog.V(3).Infof("QCloud Plugin NodeAddresses NodeName %s", name)

	addresses := make([]v1.NodeAddress, 0)

	privateIp, err := self.metaData.PrivateIPv4()
	if err != nil {
		return nil, err
	}

	glog.V(3).Infof("QCloud Plugin NodeAddresses privateIp %s", privateIp)

	addresses = append(addresses, v1.NodeAddress{
		Type: v1.NodeInternalIP, Address: privateIp,
	})

	publicIp, err := self.metaData.PublicIPv4()
	if err == nil && (publicIp != "") {
		glog.V(3).Infof("QCloud Plugin NodeAddresses publicIp %s", publicIp)
		addresses = append(addresses, v1.NodeAddress{
			Type: v1.NodeExternalIP, Address: publicIp,
		})
	}

	return addresses, nil
}

// /zone/instanceId
//只在kubelet中调用
func (self *QCloud) InstanceID(ctx context.Context, name types.NodeName) (string, error) {

	glog.V(3).Infof("QCloud Plugin InstanceID NodeName %s", name)

	instanceId, err := self.metaData.InstanceID()
	if err != nil {
		return "", err
	}

	glog.V(3).Infof("QCloud Plugin InstanceID instanceId %s", instanceId)

	zone, err := self.GetZone(ctx)
	if err != nil {
		return "", err
	}

	return "/" + zone.FailureDomain + "/" + instanceId, nil
}

//只在Master中调用
func (self *QCloud) NodeAddressesByProviderID(ctx context.Context, providerID string) ([]v1.NodeAddress, error) {

	glog.Infof("QCloud Plugin NodeAddressesByProviderID  %s", providerID)

	instanceId, err := kubernetesInstanceID(providerID).mapToInstanceID()
	if err != nil {
		return nil, err
	}

	info, err := self.getInstanceInfoByInstanceIdSingleV3(instanceId)
	if err != nil {
		return nil, err
	}

	var privateIpAddresses string
	if (len(info.PrivateIpAddresses) > 0) && (info.PrivateIpAddresses[0] != nil) {
		privateIpAddresses = *(info.PrivateIpAddresses[0])
	}

	addresses := make([]v1.NodeAddress, 0)
	addresses = append(addresses, v1.NodeAddress{
		Type: v1.NodeInternalIP, Address: privateIpAddresses,
	})

	for _, publicIpPtr := range info.PublicIpAddresses {

		var publicIp string
		if publicIpPtr != nil {
			publicIp = *publicIpPtr
		}

		addresses = append(addresses, v1.NodeAddress{
			Type: v1.NodeExternalIP, Address: publicIp,
		})
	}

	glog.Infof("QCloud Plugin NodeAddressesByProviderID addresses %v", addresses)

	return addresses, nil
}

func (self *QCloud) InstanceTypeByProviderID(ctx context.Context, providerID string) (string, error) {
	return "QCLOUD", nil
}

func (self *QCloud) InstanceType(ctx context.Context, name types.NodeName) (string, error) {
	return "QCLOUD", nil
}

func (self *QCloud) AddSSHKeyToAllInstances(ctx context.Context, user string, keyData []byte) error {
	return errors.New("AddSSHKeyToAllInstances not implemented")
}

//只会在kubelet中调用
func (self *QCloud) CurrentNodeName(ctx context.Context, hostName string) (types.NodeName, error) {

	glog.Infof("QCloud Plugin CurrentNodeName hostName %s", hostName)

	return types.NodeName(hostName), nil
}

func (self *QCloud) InstanceExistsByProviderID(ctx context.Context, providerID string) (bool, error) {
	return true, nil
}

func (self *QCloud) GetZone(ctx context.Context) (cloudprovider.Zone, error) {
	return cloudprovider.Zone{Region: self.Config.Region, FailureDomain: self.Config.Zone}, nil
}

func (self *QCloud) GetZoneByProviderID(ctx context.Context, providerID string) (cloudprovider.Zone, error) {
	return cloudprovider.Zone{Region: self.Config.Region, FailureDomain: self.Config.Zone}, nil
}
func (self *QCloud) GetZoneByNodeName(ctx context.Context, nodeName types.NodeName) (cloudprovider.Zone, error) {
	return cloudprovider.Zone{Region: self.Config.Region, FailureDomain: self.Config.Zone}, nil
}

func (self *QCloud) InstanceShutdownByProviderID(ctx context.Context, providerID string) (bool, error) {
	return true, nil
}
