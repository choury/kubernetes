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
	"bufio"
	"fmt"
	"os"
	"strconv"
	"time"
	"io"

	"github.com/dbdd4us/qcloudapi-sdk-go/metadata"
	glog "k8s.io/klog"
)

const (
	EXPIRE_TIME_SECOND_NAME    = "ExpireTimeSecond"
	DEFAULT_EXPIRE_TIME_SECOND = 15 * 60
	TIMEOUT_SECOND_NAME    = "TimeoutSecond"
	DEFAULT_TIMEOUT_SECOND_NAME = 5
	LOCAL_CACHE_PATH = "/etc/kubernetes"
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

	var errReturn error
	
	rsp, err := cached.metaData.InstanceID()
	if err != nil {
		errReturn = err
	} else {
		if rsp == "" {
			errReturn = fmt.Errorf("InstanceID cannot be empty")
		}
	}

	if errReturn == nil {
		cached.instanceId = rsp
		err := cached.SetLocalCache(metadata.INSTANCE_ID,cached.instanceId)
		if err != nil {
			glog.Warningf("SetLocalCache %s failed ,err:%s",metadata.INSTANCE_ID,err.Error())
		}
		return cached.instanceId, nil
	}else{
		glog.Errorf("InstanceID get failed ,errReturn %s",errReturn.Error())
		value,err := cached.GetLocalCache(metadata.INSTANCE_ID)
		if err != nil {
			return "",fmt.Errorf("InstanceID get failed ,errReturn %s,GetLocalCache err %s",errReturn.Error(),err.Error())
		}

		if value == ""{
			return "",fmt.Errorf("InstanceID get failed ,errReturn %s,GetLocalCache empty",errReturn.Error())
		}
		cached.instanceId = value
		return cached.instanceId,nil
	}
}

func (cached *metaDataCached) PrivateIPv4() (string, error) {
	if cached.privateIPv4 != "" {
		return cached.privateIPv4, nil
	}

	var errReturn error

	rsp, err := cached.metaData.PrivateIPv4()
	if err != nil {
		errReturn = err
	}else{
		if rsp == "" {
			errReturn = fmt.Errorf("PrivateIPv4 cannot be empty")
		}
	}

	if errReturn == nil {
		cached.privateIPv4 = rsp
		err := cached.SetLocalCache(metadata.PRIVATE_IPV4,cached.privateIPv4)
		if err != nil {
			glog.Warningf("SetLocalCache %s failed ,err:%s",metadata.PRIVATE_IPV4,err.Error())
		}
		return cached.privateIPv4, nil
	}else{
		glog.Errorf("PrivateIPv4 get failed ,errReturn %s",errReturn.Error())
		value,err := cached.GetLocalCache(metadata.PRIVATE_IPV4)
		if err != nil {
			return "",fmt.Errorf("PrivateIPv4 get failed ,errReturn %s,GetLocalCache err %s",errReturn.Error(),err.Error())
		}

		if value == ""{
			return "",fmt.Errorf("PrivateIPv4 get failed ,errReturn %s,GetLocalCache empty",errReturn.Error())
		}
		cached.privateIPv4 = value
		return cached.privateIPv4,nil
	}
}

//反回 "" 时，公网IP不存在
func (cached *metaDataCached) PublicIPv4() (string, error) {

	if (cached.publicIPv4 != nil) &&
		cached.publicIPv4LastUpdateTime.Add(time.Duration(cached.expireTimeSecond)*time.Second).After(time.Now()) {
		return *(cached.publicIPv4), nil
	}

	cached.publicIPv4LastUpdateTime = time.Now()
	
	rsp, err := cached.metaData.PublicIPv4()
	if err != nil {
		glog.Errorf("metaDataCached PublicIPv4() get err :%s", err.Error())
		if cached.publicIPv4 == nil {
			glog.Warningf("metaDataCached PublicIPv4(), use empty")
			return "", nil  //use empty to instead err,next time to update it
		} else {
			glog.Warningf("metaDataCached PublicIPv4(), use cached: %s", *(cached.publicIPv4))
			return *(cached.publicIPv4), nil
		}
	}

	cached.publicIPv4 = &rsp
	
	return *cached.publicIPv4, nil
}

func (cached *metaDataCached) SetLocalCache(resource string,value string) error {

	if resource == "" {
		return fmt.Errorf("SetLocalCache resource cannot be empty")
	}

	path := fmt.Sprintf("%s/%s",LOCAL_CACHE_PATH,resource)

	glog.Infof("SetLocalCache path %s",path)

	fout, err := os.Create(path)
	if err != nil {
		return err
	}

	defer fout.Close()

	count,err := fout.WriteString(value)
	if err != nil {
		return err
	}

	glog.Infof("SetLocalCache count %d",count)
	
	return nil
}

func (cached *metaDataCached) GetLocalCache(resource string) (string,error) {

	if resource == "" {
		return "",fmt.Errorf("GetLocalCache resource cannot be empty")
	}

	path := fmt.Sprintf("%s/%s",LOCAL_CACHE_PATH,resource)

	glog.Infof("GetLocalCache path %s",path)

	fin, err := os.Open(path)
	if err != nil {
		return "",err
	}
	defer fin.Close()

	buf := bufio.NewReader(fin)
	line, err := buf.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			glog.Infof("GetLocalCache read EOF succeed,line(%s)",line)
			return line,nil
		} else {
			return "",err
		}
	}

	glog.Infof("GetLocalCache read succeed,line(%s)",line)
	return line,nil
}
