FROM golang

RUN \
  apt update && \
  DEBIAN_FRONTEND=noninteractive \
    apt-get install -y --no-install-recommends \
      libmpv-dev \
      python-pip \
  && \
  apt-get clean && \
  rm -rf /var/lib/apt/lists/*

RUN \
  pip install --no-cache youtube-dl \
  && youtube-dl --version

WORKDIR ${GOPATH}/src/github.com/aykevl/plaincast/
COPY . ${GOPATH}/src/github.com/aykevl/plaincast/

RUN go get -v .
RUN go install -i .

ENTRYPOINT [ "plaincast" ]
