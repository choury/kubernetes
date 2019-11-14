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
	"fmt"
	"io"
	glog "k8s.io/klog"

	cloudprovider "k8s.io/cloud-provider"

	"k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"reflect"

	norm "cloud.tencent.com/tencent-cloudprovider/component"
	"cloud.tencent.com/tencent-cloudprovider/credential"

	"github.com/dbdd4us/qcloudapi-sdk-go/cbs"
	"github.com/dbdd4us/qcloudapi-sdk-go/clb"
	"github.com/dbdd4us/qcloudapi-sdk-go/common"
	"github.com/dbdd4us/qcloudapi-sdk-go/cvm"
	"github.com/dbdd4us/qcloudapi-sdk-go/snap"

	"encoding/json"
	"errors"
	"time"

	//for cbs cvm v3 yun api
	cbsv3 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs/v20170312"
	v3common "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvmv3 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
)

const (
	ProviderName                      = "qcloud"
	HostNameType                      = "hostname"
	ProviderUsedServiceAccountName    = "node-controller"
	AnnoServiceLBInternalSubnetID     = "service.kubernetes.io/qcloud-loadbalancer-internal"
	AnnoServiceLBInternalUniqSubnetID = "service.kubernetes.io/qcloud-loadbalancer-internal-subnetid"

	AnnoServiceClusterId = "service.kubernetes.io/qcloud-loadbalancer-clusterid"
)

//TODO instance cache
type QCloud struct {
	kubeClient          clientset.Interface
	currentInstanceInfo *norm.NormGetNodeInfoRsp
	metaData            *metaDataCached
	cvm                 *cvm.Client
	clb                 *clb.Client
	cbs                 *cbs.Client
	snap                *snap.Client

	cbsV3            *cbsv3.Client
	cvmV3            *cvmv3.Client
	Config           *Config
	selfInstanceInfo *cvm.InstanceInfo

	cache        *nodeCache
	listerSynced cache.InformerSynced
}

type Config struct {
	Region     string `json:"region"`
	RegionName string `json:"regionName"`
	Zone       string `json:"zone"`
	VpcId      string `json:"vpcId"`

	NodeNameType string `json:"nodeNameType"`

	QCloudSecretId  string `json:"QCloudSecretId"`
	QCloudSecretKey string `json:"QCloudSecretKey"`

	Kubeconfig string `json:"kubeconfig"`
}

var (
	config                  Config
	QcloudInstanceNotFound  = errors.New("qcloud instance not found")
	QcloudNodeNotFound      = errors.New("qcloud node not found")
	QcloudNodeLanIPNotFound = errors.New("qcloud node lanIp not found")
)

func readConfig(cfg io.Reader) error {
	if cfg == nil {
		err := fmt.Errorf("No cloud provider config given")
		return err
	}

	if err := json.NewDecoder(cfg).Decode(&config); err != nil {
		glog.Errorf("Couldn't parse config: %v", err)
		return err
	}
	glog.Infof("config:%v", config)

	return nil
}

//TODO if is master
func newQCloud() (*QCloud, error) {

	var cred common.CredentialInterface
	var cbsV3Client *cbsv3.Client
	var cvmV3Client *cvmv3.Client

	if config.QCloudSecretId == "" || config.QCloudSecretKey == "" {
		expiredDuration := time.Second * 7200

		refresher, err := credential.NewNormRefresher(expiredDuration)
		if err != nil {
			glog.Errorf("NewNormRefresher failed, %v", err)
		}
		normCredV3, err := credential.NewNormCredentialV3(expiredDuration, refresher)
		if err != nil {
			glog.Errorf("NewNormCredentialV3 failed, %v", err)
		}
		normCred, err := credential.NewNormCredential(expiredDuration, refresher)
		if err != nil {
			glog.Errorf("NewNormCredential failed, %v", err)
		}
		cred = &normCred
		cpf := profile.NewClientProfile()
		cbsV3Client, _ = cbsv3.NewClient(&normCredV3, config.RegionName, cpf)
		cvmV3Client, _ = cvmv3.NewClient(&normCredV3, config.RegionName, cpf)
	} else {
		cred = common.Credential{
			SecretId:  config.QCloudSecretId,
			SecretKey: config.QCloudSecretKey,
		}
		commonCred := v3common.NewCredential(config.QCloudSecretId, config.QCloudSecretKey)
		cpf := profile.NewClientProfile()
		cbsV3Client, _ = cbsv3.NewClient(commonCred, config.RegionName, cpf)
		cvmV3Client, _ = cvmv3.NewClient(commonCred, config.RegionName, cpf)
	}

	cvmClient, err := cvm.NewClient(
		cred,
		common.Opts{
			Region: config.Region,
		})
	if err != nil {
		return nil, err
	}
	//cvmClient.SetDebug(true)

	clbClient, err := clb.NewClient(
		cred,
		common.Opts{
			Region: config.Region,
		})
	if err != nil {
		return nil, err
	}

	cbsClient, err := cbs.NewClient(
		cred,
		common.Opts{
			Region: config.Region,
		})
	if err != nil {
		return nil, err
	}

	snapClient, err := snap.NewClient(
		cred,
		common.Opts{
			Region: config.Region,
		})
	if err != nil {
		return nil, err
	}

	cloud := &QCloud{
		Config:   &config,
		metaData: newMetaDataCached(),
		cache:    &nodeCache{nodeMap: make(map[string]*cachedNode)}, //only hostname type use
		cvm:      cvmClient,
		clb:      clbClient,
		cbs:      cbsClient,
		snap:     snapClient,
		cbsV3:    cbsV3Client,
		cvmV3:    cvmV3Client,
	}

	glog.Infof("newQCloud for qcloud cloud provider (%p)", cloud)

	return cloud, nil
}

func retrieveErrorCodeAndMessage(err error) (int, string) {
	if derr, ok := err.(*norm.RequestResultError); ok {
		return derr.Code, derr.Msg
	}
	return -999999, err.Error()
}

func init() {
	cloudprovider.RegisterCloudProvider(ProviderName, func(cfg io.Reader) (cloudprovider.Interface, error) {
		err := readConfig(cfg)
		if err != nil {
			return nil, err
		}

		return newQCloud()
	})
}

func (cloud *QCloud) Initialize(clientBuilder cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {

	glog.Infof("Initialize for qcloud cloud provider, %p", cloud)

	if cloud.IsHostNameType() {
		cloud.kubeClient = clientBuilder.ClientOrDie(ProviderUsedServiceAccountName)
	}
}

func (cloud *QCloud) IsHostNameType() bool {
	if cloud.Config.NodeNameType == HostNameType {
		return true
	}
	return false
}

// SetInformers sets informers for qcloud cloud provider.
func (cloud *QCloud) SetInformers(informerFactory informers.SharedInformerFactory) {
	glog.Infof("Setting up informers for qcloud cloud provider, %p", cloud)

	if !cloud.IsHostNameType() {
		return
	}

	nodeInformer := informerFactory.Core().V1().Nodes().Informer()
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node := obj.(*v1.Node)
			cloud.AddNodeCaches(node)
		},
		UpdateFunc: func(prev, obj interface{}) {
			prevNode := prev.(*v1.Node)
			newNode := obj.(*v1.Node)
			if !cloud.needsUpdate(prevNode, newNode) {
				return
			}
			cloud.updateNodeCaches(prevNode, newNode)
		},
		DeleteFunc: func(obj interface{}) {
			node, isNode := obj.(*v1.Node)
			// We can get DeletedFinalStateUnknown instead of *v1.Node here
			// and we need to handle that correctly.
			if !isNode {
				glog.Errorf("DeletedNode contained non-Node object: %v", obj)
				return
			}
			cloud.deleteNodeCaches(node)
		},
	})

	cloud.listerSynced = nodeInformer.HasSynced
}

// updateNodeCaches updates local cache for node's zones and external resource groups.
func (cloud *QCloud) AddNodeCaches(node *v1.Node) {

	glog.Infof("AddNodeCaches %s , %p ", node.Name, cloud)

	if v, ok := cloud.cache.get(node.Name); ok {
		glog.Warningf("AddNodeCaches %s but is already in cache", node.Name)
		cloud.updateNodeCaches(v.state, node)
		return
	} else {
		nodeUpdate := cloud.cache.getOrCreate(node)
		if nodeUpdate != nil {
			glog.Infof("AddNodeCaches %s succeed", nodeUpdate.state.Name)
		} else {
			glog.Infof("AddNodeCaches failed, nodeUpdate is empty")
		}
	}

	return
}

func (cloud *QCloud) updateNodeCaches(oldNode *v1.Node, newNode *v1.Node) {

	glog.V(4).Infof("updateNodeCaches oldNode %s newNode %s", oldNode.Name, newNode.Name)

	if oldNode.Name == newNode.Name {
		var v = cachedNode{state: newNode}
		cloud.cache.set(newNode.Name, &v)
	} else {
		glog.Warningf("updateNodeCaches oldNode %s newNode %s not equal", oldNode.Name, newNode.Name)
		cloud.cache.delete(oldNode.Name)

		var v = cachedNode{state: newNode}
		cloud.cache.set(newNode.Name, &v)
	}
}

func (cloud *QCloud) deleteNodeCaches(node *v1.Node) {

	glog.Infof("deleteNodeCaches node %s", node.Name)

	if _, ok := cloud.cache.get(node.Name); ok {
		cloud.cache.delete(node.Name)
		return
	} else {
		glog.Warningf("deleteNodeCaches node %s not in cache", node.Name)
	}
}

func (cloud *QCloud) needsUpdate(oldNode *v1.Node, newNode *v1.Node) bool {

	if !reflect.DeepEqual(oldNode.UID, newNode.UID) {
		glog.V(4).Infof("nodeName(%s,%s) UID Update", oldNode.Name, newNode.Name)
		return true
	}

	if !reflect.DeepEqual(oldNode.Spec, newNode.Spec) {
		glog.V(4).Infof("nodeName(%s,%s) Spec Update, oldNode %s ,newNode %s update", oldNode.Name, newNode.Name, oldNode.Spec.ProviderID, newNode.Spec.ProviderID)
		return true
	}

	if !reflect.DeepEqual(oldNode.Status, newNode.Status) {
		glog.V(4).Infof("nodeName(%s,%s) Status update,oldNode Addresses %#v ,newNode Addresses %#v", oldNode.Name, newNode.Name, oldNode.Status.Addresses, newNode.Status.Addresses)
		return true
	}

	return false
}

func (cloud *QCloud) getInstanceIdByNodeName(nodeName string) (string, error) {
	if v, ok := cloud.cache.get(nodeName); ok {
		instanceId, err := kubernetesInstanceID(v.state.Spec.ProviderID).mapToInstanceID()
		if err != nil {
			return "", err
		}
		return instanceId, nil
	} else {
		glog.Errorf(" QCloud getInstanceId nodeName %s not found", nodeName)
		return "", QcloudNodeNotFound
	}
}

func (cloud *QCloud) getNodeNameByLanIp(lanIp string, allNodes []*v1.Node) (string, error) {
	for _, value := range allNodes {
		for _, v := range value.Status.Addresses {
			if v.Type == v1.NodeInternalIP {
				if string(v.Address) == lanIp {
					return value.Name, nil
				}
			}
		}
	}

	return "", QcloudNodeNotFound
}

func (cloud *QCloud) getLanIpByNodeName(nodeName string) (string, error) {
	if value, ok := cloud.cache.get(nodeName); ok {
		for _, v := range value.state.Status.Addresses {
			if v.Type == v1.NodeInternalIP {
				return string(v.Address), nil
			}
		}
		return "", QcloudNodeLanIPNotFound
	} else {
		glog.Errorf(" QCloud getLanIp nodeName %s not found", nodeName)
		return "", QcloudNodeNotFound
	}
}

func (cloud *QCloud) getAllNodes() []*v1.Node {
	return cloud.cache.allNodes()
}

func (cloud *QCloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	return cloud, true
}

func (cloud *QCloud) Instances() (cloudprovider.Instances, bool) {
	return cloud, true
}

func (cloud *QCloud) Zones() (cloudprovider.Zones, bool) {
	return cloud, true
}

func (cloud *QCloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

func (cloud *QCloud) Routes() (cloudprovider.Routes, bool) {
	return cloud, true
}

func (cloud *QCloud) ProviderName() string {
	return ProviderName
}

func (cloud *QCloud) ScrubDNS(nameservers, seraches []string) ([]string, []string) {
	return nameservers, seraches
}

func (cloud *QCloud) HasClusterID() bool {
	return false
}
