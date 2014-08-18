# some helpful shortcuts

build:
	go install github.com/aykevl93/youtube-receiver

fmt:
	go fmt . ./apps ./apps/youtube ./apps/youtube/mp ./server

run: build
	$(GOPATH)/bin/youtube-receiver
