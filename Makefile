# some helpful shortcuts

build:
	go install github.com/aykevl/plaincast

fmt:
	go fmt . ./apps ./apps/youtube ./apps/youtube/mp ./config ./log ./server

run: build
	$(GOPATH)/bin/plaincast
