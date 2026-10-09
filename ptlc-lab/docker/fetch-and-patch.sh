#!/bin/sh
# fetch-and-patch REPO BASE TREE PATCHES OUT
#
# Fetches one commit of REPO, applies every patch in PATCHES in name order, and
# refuses to go on unless the resulting tree is exactly TREE, the tree of the
# branch the patches were exported from. OUT receives the files, without .git.
#
# PATCHES may hold no patch at all: BASE is then the branch itself, taken
# whole, and TREE is still what it must come to.
set -eu
repo=$1 base=$2 tree=$3 patches=$4 out=$5

work=$(mktemp -d)
cd "$work"
git init -q .
# The patches carry the files as the branch has them; nothing here may
# renormalise a line ending on the way through.
git config core.autocrlf false
git remote add origin "$repo"
git fetch -q --depth 1 origin "$base"
git checkout -q FETCH_HEAD
list=$(find "$patches" -maxdepth 1 -name '*.patch' | sort)
for p in $list; do
  git apply --index --whitespace=nowarn "$p"
done
got=$(git write-tree)
if [ "$got" != "$tree" ]; then
  echo "patched tree is $got, the branch's is $tree: the patches and the branch have drifted" >&2
  exit 1
fi
mkdir -p "$out"
git checkout-index -a -f --prefix="$out/"
echo "$repo @ $base + $(echo "$list" | grep -c .) patches = tree $got"
