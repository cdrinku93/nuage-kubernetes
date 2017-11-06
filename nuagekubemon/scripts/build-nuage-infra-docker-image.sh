#!/bin/bash

set -e

if [ -z ${version} ]; then
    echo "\"version\" environmental variable is not set";
    exit 1
fi

sudo docker build -t nuage/infra:${version} .
docker save nuage/infra:${version} > nuage-infra-docker-${version}.tar
docker rmi nuage/infra:${version}