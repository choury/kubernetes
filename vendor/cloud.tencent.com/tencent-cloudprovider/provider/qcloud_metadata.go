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
	"os"
	"strconv"
	"time"
	
	"github.com/dbdd4us/qcloudapi-sdk-go/metadata"
	glog "k8s.io/klog"
)

const (
	EXPIRE_TIME_SECOND_NAME    = "ExpireTimeSecond"
	DEFAULT_EXPIRE_TIME_SECOND = 15 * 60
	TIMEOUT_SECOND_NAME    = "TimeoutSecond"
	DEFAULT_TIMEOUT_SECOND_NAME = 5
)

//避免对metadata服务的强依赖
//假设 instanceId和PrivateIP是不变的，故优先从cache中获取
//public优先从metadata中获取

type metaDataCached struct {
	metaData    *metadata.MetaData
	instanceId  string
	privateIPv4 string
	publicIPv4  *string // 可能为nil

	publicIPv4LastUpdateTime time.Time
	expireTimeSecond         int64
}

func newMetaDataCached() *metaDataCached {
	var expireTimeSecond = int64(DEFAULT_EXPIRE_TIME_SECOND)
	var timeoutSecond = uint64(DEFAULT_TIMEOUT_SECOND_NAME)

	{
		if envStr := os.Getenv(EXPIRE_TIME_SECOND_NAME); envStr != "" {
			glog.Infof("EXPIRE_TIME_SECOND_NAME: %s env is %s ", EXPIRE_TIME_SECOND_NAME, envStr)
			value, err := strconv.ParseInt(envStr, 10, 64)
			if err != nil {
				glog.Warningf("EXPIRE_TIME_SECOND_NAME envStr %s transfer failed,err:%s", envStr, err.Error())
			} else {
				if value > 0 {
					expireTimeSecond = value
				}
			}
		} else {
			glog.Infof("EXPIRE_TIME_SECOND_NAME: %s env is  empty ", EXPIRE_TIME_SECOND_NAME)
		}

		glog.Infof("expireTimeSecond %d", expireTimeSecond)
	}

	{
		if envTimeoutStr := os.Getenv(TIMEOUT_SECOND_NAME); envTimeoutStr != "" {
			glog.Infof("TIMEOUT_SECOND_NAME: %s env is %s ", TIMEOUT_SECOND_NAME, envTimeoutStr)
			value, err := strconv.ParseUint(envTimeoutStr, 10, 64)
			if err != nil {
				glog.Warningf("TIMEOUT_SECOND_NAME envTimeoutStr %s transfer failed,err:%s", envTimeoutStr, err.Error())
			} else {
				if value > uint64(0) {
					timeoutSecond = value
				}
			}
		} else {
			glog.Infof("TIMEOUT_SECOND_NAME: %s env is  empty ", TIMEOUT_SECOND_NAME)
		}

		glog.Infof("timeoutSecond %d", timeoutSecond)
	}


	return &metaDataCached{
		metaData: metadata.NewMetaData(nil,timeoutSecond),
		expireTimeSecond:expireTimeSecond,
	}
}

func (cached *metaDataCached) InstanceID() (string, error) {
	if cached.instanceId != "" {
		return cached.instanceId, nil
	}
	rsp, err := cached.metaData.InstanceID()
	if err != nil {
		return "", err
	}

	if rsp == "" {
		return "",fmt.Errorf("InstanceID cannot be empty")
	}

	cached.instanceId = rsp
	return cached.instanceId, nil
}

func (cached *metaDataCached) PrivateIPv4() (string, error) {
	if cached.privateIPv4 != "" {
		return cached.privateIPv4, nil
	}
	rsp, err := cached.metaData.PrivateIPv4()
	if err != nil {
		return "", err
	}

	if rsp == "" {
		return "",fmt.Errorf("PrivateIPv4 cannot be empty")
	}

	cached.privateIPv4 = rsp
	return cached.privateIPv4, nil
}

//反回 "" 时，公网IP不存在
func (cached *metaDataCached) PublicIPv4() (string, error) {

	if (cached.publicIPv4 != nil) &&
		cached.publicIPv4LastUpdateTime.Add(time.Duration(cached.expireTimeSecond)*time.Second).After(time.Now()) {
		return *(cached.publicIPv4), nil
	}

	rsp, err := cached.metaData.PublicIPv4()
	if err != nil {
		glog.Errorf("metaDataCached PublicIPv4() get err :%s", err.Error())
		if cached.publicIPv4 == nil {
			return "", err
		} else {
			glog.Warningf("metaDataCached PublicIPv4(), use cached: %s", *(cached.publicIPv4))
			return *(cached.publicIPv4), nil
		}
	}

	cached.publicIPv4 = &rsp
	cached.publicIPv4LastUpdateTime = time.Now()

	return *cached.publicIPv4, nil
}
