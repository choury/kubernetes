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
	norm "cloud.tencent.com/tencent-cloudprovider/component"
	glog "k8s.io/klog"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
	"context"
)

func (self *QCloud) ListRoutes(ctx context.Context, clusterName string) ([]*cloudprovider.Route, error) {

	glog.Infof("QCloud Plugin ListRoutes clusterName %s", clusterName)

	routes := make([]*cloudprovider.Route, 0)
	req := norm.NormListRoutesReq{ClusterName: clusterName}
	rsp, err := norm.NormListRoutes(req)
	if err != nil {
		return nil, err
	}

	if !self.IsHostNameType() {
		for _, item := range rsp.Routes {
			if item.Subnet == "" {
				continue
			}
			routes = append(routes, &cloudprovider.Route{
				Name:            "", // TODO: what's this?
				TargetNode:      types.NodeName(item.Name),
				DestinationCIDR: item.Subnet,
			})
		}
	} else {
		allNodes := self.getAllNodes()
		for _, item := range rsp.Routes {
			if item.Subnet == "" {
				continue
			}

			nodeName, err := self.getNodeNameByLanIp(item.Name, allNodes)
			if err != nil {
				glog.Errorf("getNodeNameByLanIp %s failed %s", item.Name, err.Error())
				continue
			}

			routes = append(routes, &cloudprovider.Route{
				Name:            "", // TODO: what's this?
				TargetNode:      types.NodeName(nodeName),
				DestinationCIDR: item.Subnet,
			})
		}
	}

	return routes, nil
}

func (self *QCloud) CreateRoute(ctx context.Context, clusterName string, nameHint string, route *cloudprovider.Route) error {
	glog.Infof("QCloud Plugin CreateRoute clusterName %s nameHint %s nodeName %s", clusterName, nameHint, string(route.TargetNode))

	if !self.IsHostNameType() {
		req := []norm.NormRouteInfo{
			{Name: string(route.TargetNode), Subnet: route.DestinationCIDR},
		}
		_, err := norm.NormAddRoute(req)
		if err != nil {
			glog.Errorf("CreateRoute nodeName %s NormAddRoute failed %s", string(route.TargetNode), err.Error())
			return err
		}
	} else {
		lanIp, err := self.getLanIpByNodeName(string(route.TargetNode))
		if err != nil {
			glog.Errorf("CreateRoute nodeName %s getLanIpByNodeName failed %s", string(route.TargetNode), err.Error())
			return err
		}

		req := []norm.NormRouteInfo{
			{Name: string(lanIp), Subnet: route.DestinationCIDR},
		}

		_, err = norm.NormAddRoute(req)
		if err != nil {
			glog.Errorf("CreateRoute nodeName %s  lanIp %s NormAddRoute(2)failed %s", string(route.TargetNode), lanIp, err.Error())
			return err
		}
	}

	return nil
}

// DeleteRoute deletes the specified managed route
// Route should be as returned by ListRoutes
func (self *QCloud) DeleteRoute(ctx context.Context, clusterName string, route *cloudprovider.Route) error {

	glog.Infof("QCloud Plugin DeleteRoute clusterName %s nodeName %s", clusterName, string(route.TargetNode))

	if !self.IsHostNameType() {
		req := []norm.NormRouteInfo{
			{Name: string(route.TargetNode), Subnet: route.DestinationCIDR},
		}
		_, err := norm.NormDelRoute(req)

		if err != nil {
			glog.Errorf("DeleteRoute nodeName %s NormDelRoute failed %s", string(route.TargetNode), err.Error())
			return err
		}
	} else {
		lanIp, err := self.getLanIpByNodeName(string(route.TargetNode))
		if err != nil {
			glog.Errorf("CreateRoute nodeName %s getLanIpByNodeName failed %s", string(route.TargetNode), err.Error())
			return err
		}

		req := []norm.NormRouteInfo{
			{Name: string(lanIp), Subnet: route.DestinationCIDR},
		}

		_, err = norm.NormDelRoute(req)
		if err != nil {
			glog.Errorf("DeleteRoute nodeName %s  lanIp %s NormDelRoute(2) failed %s", string(route.TargetNode), lanIp, err.Error())
			return err
		}
	}

	return nil
}
