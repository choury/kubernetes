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
	cvmv3 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	glog "k8s.io/klog"
)

var (
	FilterNamePrivateIpAddress = "private-ip-address"
	FilterNameVpcID            = "vpc-id"
)

func (qcloud *QCloud) getInstanceInfoByNodeNameV3(nodeName string) (*cvmv3.Instance, error) {
	if qcloud.Config.NodeNameType != HostNameType {
		return qcloud.getInstanceInfoByLanIpV3(nodeName)
	} else {
		instanceId, err := qcloud.getInstanceIdByNodeName(nodeName)
		if err != nil {
			glog.Errorf("getInstanceIdByNodeName failed %s", err.Error())
			return nil, err
		}
		return qcloud.getInstanceInfoByInstanceIdSingleV3(instanceId)
	}
}

func (qcloud *QCloud) getInstanceInfoByLanIpV3(lanIP string) (*cvmv3.Instance, error) {
	vpcIdFilter := cvmv3.Filter{&FilterNameVpcID, []*string{&qcloud.Config.VpcId}}
	privateIPFilter := cvmv3.Filter{&FilterNamePrivateIpAddress, []*string{&lanIP}}
	descRequest := cvmv3.NewDescribeInstancesRequest()
	descRequest.Filters = []*cvmv3.Filter{&vpcIdFilter, &privateIPFilter}

	resp, err := qcloud.cvmV3.DescribeInstances(descRequest)
	if err != nil {
		return nil, err
	}

	for _, instance := range resp.Response.InstanceSet {

		if stringInV3(lanIP, instance.PrivateIpAddresses) {
			return instance, nil
		}
	}
	return nil, QcloudInstanceNotFound
}

func (qcloud *QCloud) getInstanceInfoByInstanceIdSingleV3(instanceId string) (*cvmv3.Instance, error) {

	descRequest := cvmv3.NewDescribeInstancesRequest()
	descRequest.InstanceIds = []*string{&instanceId}

	glog.V(2).Infof("getInstanceInfoByInstanceIdSingleV3 instanceId %s descRequest %s", instanceId, descRequest.ToJsonString())

	resp, err := qcloud.cvmV3.DescribeInstances(descRequest)
	if err != nil {
		glog.Errorf("DescribeInstances failed %s", err.Error())
		return nil, err
	}

	if (resp == nil) || (resp.Response == nil) {
		glog.Errorf("DescribeInstances failed resp or resp.Response is empty")
		return nil, QcloudInstanceNotFound
	}

	if len(resp.Response.InstanceSet) == 0 {
		glog.Errorf("DescribeInstances failed not found %s instance", instanceId)
		return nil, QcloudInstanceNotFound
	}

	glog.V(2).Infof("getInstanceInfoByInstanceIdSingleV3 instanceId %s Response %s", instanceId, resp.ToJsonString())

	return resp.Response.InstanceSet[0], nil
}
