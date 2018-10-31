#!/bin/sh

for os in linux windows darwin openbsd netbsd freebsd; do
  GOOS=$os GOARCH=amd64 go build -o build/svglinkify-$os-amd64
done
