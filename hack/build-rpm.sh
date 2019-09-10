#!/bin/bash

# Copyright 2016 The Kubernetes Authors All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# RPM package deployment

set -o errexit
set -o nounset
set -o pipefail

KUBE_ROOT=$(dirname "${BASH_SOURCE}")/..
source "$KUBE_ROOT/build/common.sh"

# The set of source targets to include in the kube-build image
function kube::build::source_targets() {
  local targets=(
      $(find . -mindepth 1 -maxdepth 1 -not \(        \
          \( -path ./_\* -o -path ./.git\* \) -prune  \
        \))
  )
  echo "${targets[@]}"
}

function kube::build::prepare_build() {
  kube::build::ensure_tar

  mkdir -p "${LOCAL_OUTPUT_BUILD_CONTEXT}"

  "${TAR}" hczf "${LOCAL_OUTPUT_BUILD_CONTEXT}/kube-source.tar.gz" --transform 's,^,/k8s-'$KUBE_VERSION'/,' $(kube::build::source_targets)

  cp -R "${KUBE_ROOT}/build/systemd/k8s.spec" "${LOCAL_OUTPUT_BUILD_CONTEXT}"

  cat > "${LOCAL_OUTPUT_BUILD_CONTEXT}/Dockerfile" << EOF
FROM ccr.ccs.tencentyun.com/timxbxu/tk8s-builder:v1.14.3
RUN mkdir -p ~/rpmbuild/{BUILD,RPMS,SOURCES,SPECS,SRPMS,tmp}
COPY k8s.spec /root/rpmbuild/SPECS
COPY kube-source.tar.gz /root/rpmbuild/SOURCES
RUN echo '%_topdir /root/rpmbuild' > /root/.rpmmacros \
          && echo '%__os_install_post %{nil}' >> /root/.rpmmacros \
                  && echo '%debug_package %{nil}' >> /root/.rpmmacros
WORKDIR /root/rpmbuild/SPECS
RUN rpmbuild -bb \
  --define 'version ${KUBE_VERSION}' \
  --define 'release ${TK8S_RELEASE}' \
  k8s.spec
EOF
}

function kube::build::generate() {
  kube::log::status "Generating RPM packages..."
  ( cd "${LOCAL_OUTPUT_BUILD_CONTEXT}" && docker build -t "$RPM_IMAGE" . )
  docker run --rm "$RPM_IMAGE" bash -c 'cd /root/rpmbuild && tar -c RPMS' | tar xvC "${LOCAL_OUTPUT_BUILD_CONTEXT}"
}

function kube::build::cleanup() {
  kube::log::status "Cleaning up temporary images..."
  docker rmi "$RPM_IMAGE"
}

trap kube::build::cleanup EXIT SIGINT SIGTERM

KUBE_GIT_VERSION_FILE="${KUBE_ROOT}/build/.kube-version-defs"
if [[ -f ${KUBE_GIT_VERSION_FILE} ]]; then
  kube::version::get_version_vars
else
  KUBE_GIT_VERSION_FILE=""
  kube::version::get_version_vars
  KUBE_GIT_VERSION_FILE="${KUBE_ROOT}/build/.kube-version-defs"
  kube::version::save_version_vars ${KUBE_GIT_VERSION_FILE}
fi

git=(git --work-tree "${KUBE_ROOT}")
KUBE_VERSION=$("${git[@]}" describe --tags --abbrev=0)
TK8S_RELEASE=${KUBE_VERSION##*-}
KUBE_VERSION=${KUBE_VERSION%%-*}
RPM_IMAGE="k8s-${KUBE_VERSION}:${TK8S_RELEASE}"

kube::build::verify_prereqs
kube::build::prepare_build
kube::build::generate