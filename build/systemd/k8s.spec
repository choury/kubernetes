Name: Tencent-Kubernetes
Version: %{version}
Release: %{release}%{?dist}
Summary: An open source system for managing containerized applications across multiple hosts, providing basic mechanisms for deployment, maintenance, and scaling of applications.

Group: Development/TK8S
License: MIT
Source: kube-source.tar.gz

Requires: iptables >= 1.4.21, conntrack, ipvsadm, ipset
Requires: systemd-units

%description
An open source system for managing containerized applications across multiple hosts, providing basic mechanisms for deployment, maintenance, and scaling of applications.

%define pkgname %{name}-%{version}-%{release}

%prep
%setup -n k8s-%{version}
cat <<EOF >> ./build/.kube-version-defs
KUBE_PKG_NAME='%{pkgname}'
EOF

cat ./build/.kube-version-defs

%build
export KUBE_GIT_VERSION_FILE=./build/.kube-version-defs
make WHAT='cmd/kube-proxy cmd/kube-apiserver cmd/kube-controller-manager cmd/kubelet cmd/kube-scheduler cmd/kubectl'

%install
install -d $RPM_BUILD_ROOT/%{_bindir}
install -d $RPM_BUILD_ROOT/%{_unitdir}
install -d $RPM_BUILD_ROOT/etc/kubernetes

components=(
kube-proxy
kube-apiserver
kube-controller-manager
kubelet
kube-scheduler
kubectl
)

services=(
kube-apiserver
kube-controller-manager
kubelet
kube-proxy
kube-scheduler
)

for cpt in "${components[@]}";do
  install -p -m 755 _output/bin/${cpt} $RPM_BUILD_ROOT/%{_bindir}/${component}
done

for svc in "${services[@]}";do
  install -p -m 644 ./build/systemd/${svc} $RPM_BUILD_ROOT/etc/kubernetes
  install -p -m 644 ./build/systemd/${svc}.service $RPM_BUILD_ROOT/%{_unitdir}/
done

%clean
rm -rf $RPM_BUILD_ROOT

%package -n kubernetes-master-tk8s
Requires: systemd-units
Summary: An open source system for managing containerized applications across multiple hosts, providing basic mechanisms for deployment, maintenance, and scaling of applications.
%description -n kubernetes-master-tk8s

%package -n kubernetes-node-tk8s
Requires: iptables >= 1.4.21, contrack-tools, ipset, ipvsadm
Requires: systemd-units
Summary: An open source system for managing containerized applications across multiple hosts, providing basic mechanisms for deployment, maintenance, and scaling of applications.
%description -n kubernetes-node-tk8s

%package -n kubernetes-client-tk8s
Summary: An open source system for managing containerized applications across multiple hosts, providing basic mechanisms for deployment, maintenance, and scaling of applications.
%description -n kubernetes-client-tk8s

%files
%config(noreplace,missingok) /etc/kubernetes

/%{_unitdir}/kube-apiserver.service
/%{_unitdir}/kube-controller-manager.service
/%{_unitdir}/kubelet.service
/%{_unitdir}/kube-proxy.service
/%{_unitdir}/kube-scheduler.service

/%{_bindir}/kube-proxy
/%{_bindir}/kube-apiserver
/%{_bindir}/kube-controller-manager
/%{_bindir}/kubelet
/%{_bindir}/kube-scheduler
/%{_bindir}/kubectl

%files -n kubernetes-master-tk8s
%config(noreplace,missingok) /etc/kubernetes/kube-apiserver
%config(noreplace,missingok) /etc/kubernetes/kube-controller-manager
%config(noreplace,missingok) /etc/kubernetes/kube-scheduler

/%{_unitdir}/kube-apiserver.service
/%{_bindir}/kube-apiserver

/%{_unitdir}/kube-controller-manager.service
/%{_bindir}/kube-controller-manager

/%{_unitdir}/kube-scheduler.service
/%{_bindir}/kube-scheduler

%files -n kubernetes-node-tk8s
%config(noreplace,missingok) /etc/kubernetes/kube-proxy
%config(noreplace,missingok) /etc/kubernetes/kubelet

/%{_unitdir}/kube-proxy.service
/%{_bindir}/kube-proxy

/%{_unitdir}/kubelet.service
/%{_bindir}/kubelet

%files -n kubernetes-client-tk8s
/%{_bindir}/kubectl