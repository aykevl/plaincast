# some helpful shortcuts

build:
	go install github.com/aykevl/plaincast

fmt:
	go fmt . ./apps ./apps/youtube ./apps/youtube/mp ./config ./log ./server

run: build
	${GOPATH}bin/plaincast

install:
	cp ${GOPATH}bin/plaincast /usr/local/bin/plaincast.new
	mv /usr/local/bin/plaincast.new /usr/local/bin/plaincast
	if ! egrep -q "^plaincast:" /etc/passwd; then useradd -s /bin/false -r -M plaincast -g audio; fi
	mkdir -p /var/local/plaincast
	chown plaincast:audio /var/local/plaincast
	cp $(CURDIR)/plaincast.service /etc/systemd/system/plaincast.service
	systemctl enable plaincast

remove:
	rm -f /usr/local/bin/plaincast
	if egrep -q "^plaincast:" /etc/passwd; then userdel plaincast; fi
	# rm -rf /var/local/plaincast # this removes configuration files
	systemctl disable plaincast
	rm -f /etc/systemd/system/plaincast.service
