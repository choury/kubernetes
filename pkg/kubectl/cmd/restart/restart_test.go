/*
Copyright 2019 The Tencent Inc.

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

package restart

import (
	"testing"

	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/rest/fake"
	cmdtesting "k8s.io/kubernetes/pkg/kubectl/cmd/testing"
	"k8s.io/kubernetes/pkg/kubectl/scheme"
)

type fakeRestarter struct {
	url    string
	period string
}

func (r *fakeRestarter) Restart(req *rest.Request) error {
	r.url = req.URL().Path
	r.period = req.URL().Query().Get("terminationGracePeriodSeconds")
	return nil
}

func TestPodRestart(t *testing.T) {
	tests := []struct {
		name         string
		pod          *corev1.Pod
		podPath      string
		period       string
		expectPeriod bool
	}{
		{
			name:    "pod restart",
			pod:     execPod(),
			period:  "-1",
			podPath: "/api/v1/namespaces/" + execPod().Namespace + "/pods/" + execPod().Name + "/restart",
		},
		{
			name:         "pod restart with period",
			pod:          execPod(),
			period:       "30",
			podPath:      "/api/v1/namespaces/" + execPod().Namespace + "/pods/" + execPod().Name + "/restart",
			expectPeriod: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			tf := cmdtesting.NewTestFactory().WithNamespace("test")
			defer tf.Cleanup()

			ns := scheme.Codecs

			tf.Client = &fake.RESTClient{
				VersionedAPIPath:     "/api/v1",
				GroupVersion:         schema.GroupVersion{Group: "", Version: "v1"},
				NegotiatedSerializer: ns,
			}
			tf.ClientConfigVal = cmdtesting.DefaultClientConfig()
			opts := &RestartOptions{}
			opts.restarter = &fakeRestarter{}
			cmd := NewCmdRestart(tf, genericclioptions.NewTestIOStreamsDiscard())
			cmd.Run = func(cmd *cobra.Command, args []string) {
				if err = opts.Complete(tf, cmd, args); err != nil {
					return
				}
				err = opts.Restart(tf, cmd, args)
			}
			cmd.Flags().Set("grace-period", test.period)
			cmd.Run(cmd, []string{test.pod.Name})

			if err != nil {
				t.Errorf("%s: Unexpected error: %v", test.name, err)
			}
			if test.podPath != opts.restarter.(*fakeRestarter).url {
				t.Errorf("%s: unexpected url: %s vs %s", test.name, test.podPath, opts.restarter.(*fakeRestarter).url)
			}
			if test.expectPeriod && test.period != opts.restarter.(*fakeRestarter).period {
				t.Errorf("%s: unexpected period: %s vs %s", test.name, test.period, opts.restarter.(*fakeRestarter).period)
			}
		})
	}
}

func execPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "test", ResourceVersion: "10"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			DNSPolicy:     corev1.DNSClusterFirst,
			Containers: []corev1.Container{
				{
					Name: "bar",
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
}
