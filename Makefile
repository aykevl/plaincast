# some helpful shortcuts

build:
	GOPATH="`pwd`"/../.. go install youtube-receiver

fmt:
	go fmt . ./apps ./apps/youtube ./apps/youtube/mp ./server

run: build
	../../bin/youtube-receiver
