#!/usr/bin/env bash

#############################################################################
# This script assumes you have jekyll installed on your system. If this is  #
# not true, then please install jekyll using instructions in docs           #
# This script is meant to be run before rpm-prepare.sh but can be used in   #
# standalone mode as well.                                                  #
#############################################################################

export ODO_REPO_URL="https://github.com/openshift/odo"
export ODO_GH_PAGES="gh-pages"
export ODO_GIT_REMOTE="site-packager"
export REPO_LOCATION=`mktemp -d`

set -e

if [[ ! -x `which jekyll` ]]; then
  echo "Jekyll is required to build the site and is not setup. Please follow documentation on how to setup jekyll on your system"
  exit 1
fi

echo "Cloning repo locally to temp location $REPO_LOCATION"
git clone --depth 1  `pwd` $REPO_LOCATION

echo "Setting up remote to fetch $ODO_GH_PAGES from $ODO_REPO_URL"
pushd $REPO_LOCATION
git remote add $ODO_GIT_REMOTE $ODO_REPO_URL
git fetch $ODO_GIT_REMOTE
git checkout $ODO_GIT_REMOTE/$ODO_GH_PAGES

echo "Building and packaging the site"
jekyll build
cp site-readme.txt _site/README.txt
pushd _site
tar -zcvf site.tar.gz *
popd
popd

if [[ ! -d "./dist" ]]; then
  mkdir ./dist
fi
cp -avrf $REPO_LOCATION/_site/site.tar.gz ./dist/
echo "Site should be availabe in ./dist/site.tar.gz"
