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
	"io/ioutil"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	restclient "k8s.io/client-go/rest"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/scheme"
	"k8s.io/kubernetes/pkg/kubectl/util/i18n"
	"k8s.io/kubernetes/pkg/kubectl/util/templates"
	kubelettypes "k8s.io/kubernetes/pkg/kubelet/types"
)

// RestartOptions contains all the options for restart/shutdown/start cli command
type RestartOptions struct {
	Namespace          string
	PodName            string
	GracePeriodSeconds int64
	Unshutdown         bool
	PodClient          corev1client.PodsGetter
	RESTClient         *restclient.RESTClient
	restarter          restarter

	genericclioptions.IOStreams
}

type restarter interface {
	Restart(*rest.Request) error
}

var (
	restartExample = templates.Examples(i18n.T(`
		# Restart pod nginx and wait util succeed or failed
		kubectl restart nginx

		# Restart pod nginx with terminated period second of 60
		kubectl restart nginx --grace-period=60`))

	shutdownExample = templates.Examples(i18n.T(`
		# Shutdown pod nginx and wait util succeed or failed
		kubectl shutdown nginx

		# Shutdown pod nginx with terminated period second of 60
		kubectl shutdown nginx --grace-period=60`))

	startExamle = templates.Examples(i18n.T(`
		# Unshutdown pod nginx
		kubectl start nginx`))
)

// NewCmdRestart creates a new pod restart command
func NewCmdRestart(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := &RestartOptions{
		restarter: &defaultRestarter{},
		IOStreams: streams,
	}
	cmd := &cobra.Command{
		Use:     "restart POD",
		Short:   i18n.T("Restart a pod"),
		Long:    "Restart a pod.",
		Example: restartExample,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Complete(f, cmd, args))
			cmdutil.CheckErr(o.Restart(f, cmd, args))
		},
	}
	cmd.Flags().Int64("grace-period", -1, "Period of time in seconds given to the resource to terminate gracefully. Ignored if negative.")
	return cmd
}

func NewCmdShutdown(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := &RestartOptions{
		restarter: &defaultRestarter{},
		IOStreams: streams,
	}
	cmd := &cobra.Command{
		Use:     "shutdown POD",
		Short:   i18n.T("Shutdown a pod"),
		Long:    "Shutdown a pod.",
		Example: shutdownExample,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Complete(f, cmd, args))
			cmdutil.CheckErr(o.markShutdown(f, true))
			cmdutil.CheckErr(o.Restart(f, cmd, args))
		},
	}
	cmd.Flags().Int64("grace-period", -1, "Period of time in seconds given to the resource to terminate gracefully. Ignored if negative.")
	return cmd
}

func NewCmdStart(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := &RestartOptions{
		restarter:  &defaultRestarter{},
		Unshutdown: true,
		IOStreams:  streams,
	}
	cmd := &cobra.Command{
		Use:     "start POD",
		Short:   i18n.T("UnShutdown a pod"),
		Long:    "UnShutdown a pod.",
		Example: startExamle,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Complete(f, cmd, args))
			cmdutil.CheckErr(o.markShutdown(f, false))
		},
	}
	return cmd
}

func (o *RestartOptions) Complete(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	var err error
	if len(args) == 0 {
		return cmdutil.UsageErrorf(cmd, "POD is required for restart")
	}

	o.PodName = args[0]
	o.Namespace, _, err = f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}
	if err != nil {
		return err
	}

	if !o.Unshutdown {
		o.GracePeriodSeconds = cmdutil.GetFlagInt64(cmd, "grace-period")
	}
	o.RESTClient, err = f.RESTClient()
	if err != nil {
		return err
	}

	clientset, err := f.KubernetesClientSet()
	if err != nil {
		return err
	}
	o.PodClient = clientset.CoreV1()
	return nil
}

type defaultRestarter struct{}

func (r *defaultRestarter) Restart(req *rest.Request) error {
	readCloser, err := req.Stream()
	if err != nil {
		return err
	}

	defer readCloser.Close()

	_, err = ioutil.ReadAll(readCloser)
	if err != nil {
		return err
	}
	return nil
}

// Restart implements all the necessary functionality for restart cmd.
func (o *RestartOptions) Restart(f cmdutil.Factory, cmd *cobra.Command, args []string) error {
	var opts corev1.PodRestartOptions
	if o.GracePeriodSeconds >= 0 {
		opts.TerminationGracePeriodSeconds = &o.GracePeriodSeconds
	}
	req := o.RESTClient.Post().
		Resource("pods").
		Namespace(o.Namespace).
		Name(o.PodName).
		SubResource("restart").
		VersionedParams(&opts, scheme.ParameterCodec)

	return o.restarter.Restart(req)
}

func (o *RestartOptions) markShutdown(f cmdutil.Factory, shutdown bool) error {
	pod, err := o.PodClient.Pods(o.Namespace).Get(o.PodName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_, ok := pod.Annotations[kubelettypes.PodShutdownAnnotationKey]
	if !ok && !shutdown {
		return nil
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	if shutdown {
		pod.Annotations[kubelettypes.PodShutdownAnnotationKey] = "true"
	} else {
		pod.Annotations[kubelettypes.PodShutdownAnnotationKey] = "false"
	}
	_, err = o.PodClient.Pods(o.Namespace).Update(pod)
	return err
}
