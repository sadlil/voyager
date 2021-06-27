#!/bin/bash

# Copyright AppsCode Inc. and Contributors
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

export KLOADER_ARGS="$@"
export >/etc/envvars

[[ $DEBUG == true ]] && set -x

# create haproxy.cfg dir
mkdir /etc/haproxy

CERT_DIR=/etc/ssl/private/haproxy
mkdir -p /etc/ssl/private/haproxy

# http://stackoverflow.com/a/2108296
for dir in /srv/haproxy/secrets/*/; do
    # remove trailing /
    dir=${dir%*/}
    # just basename
    secret=${dir##*/}

    cat $dir/tls.crt >$CERT_DIR/$secret.pem
    cat $dir/tls.key >>$CERT_DIR/$secret.pem
done

echo "Checking HAProxy configuration ..."
cmd="/kloader check $KLOADER_ARGS"
echo $cmd
$cmd
rc=$?
if [[ $rc != 0 ]]; then exit $rc; fi

echo "Starting runit..."
exec /usr/sbin/runsvdir-start
