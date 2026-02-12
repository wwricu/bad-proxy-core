FROM alpine:latest

WORKDIR /root
ADD https://github.com/HerrKKK/bad_proxy_go/releases/latest/download/bad_proxy-linux-amd64.tar.gz .

RUN apk add --no-cache ca-certificates \
&& tar xzvf bad_proxy-linux-amd64.tar.gz && rm bad_proxy-linux-amd64.tar.gz \
&& mkdir /etc/bad_proxy && mv ./rules.dat /etc/bad_proxy/ \
&& mv ./bad_proxy-linux-amd64 /usr/bin/bad_proxy

CMD ["/usr/bin/bad_proxy", "--config", "/etc/bad_proxy/config.json", "--router-path", "/etc/bad_proxy/rules.dat"]
