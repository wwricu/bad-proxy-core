FROM debian:stable-slim

ADD https://github.com/HerrKKK/bad_proxy_go/releases/latest/download/bad_proxy-linux-amd64.tar.gz /root

RUN cd /root && tar xzvf bad_proxy-linux-amd64.tar.gz \
&& mkdir /etc/bad_proxy && rm bad_proxy-linux-amd64.tar.gz \
&& cp ./bad_proxy-linux-amd64 /usr/bin/bad_proxy \
&& cp ./rules.dat /etc/bad_proxy/rules.dat \
&& ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
&& echo 'Asia/Shanghai' > /etc/timezone

CMD ["/usr/bin/bad_proxy", "--config", "/etc/bad_proxy/config.json", "--router-path", "/etc/bad_proxy/rules.dat"]
