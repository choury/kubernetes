/*
Copyright 2014 The Kubernetes Authors.

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

package network

import (
	"os"
)

// the infacename will be readed from the config that the systemd used to start the kubelet service
// if there is no value from the config , the interface name will be set in default value "eth0"
// there may can provide a interface to get the interface name from the TKE console
// if need change the interface name in runtime , the pods created before changing need restart
var  DefaultInterfaceName = getInterfaceName()

// UseDefaultMTU is a marker value that indicates the plugin should determine its own MTU
// It is the zero value, so a non-initialized value will mean "UseDefault"
const UseDefaultMTU = 0

func getInterfaceName() string{
	interfaceName := os.Getenv("INTERFACE_NAME")
	if interfaceName == "" {
		interfaceName = "eth0"
	}
	return interfaceName
}
