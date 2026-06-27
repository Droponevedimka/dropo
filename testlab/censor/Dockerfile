FROM debian:stable-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        iptables iproute2 python3 procps ca-certificates \
    && rm -rf /var/lib/apt/lists/*
# use legacy iptables (xt_string match lives here)
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy || true
COPY apply.py /apply.py
ENTRYPOINT ["python3", "/apply.py"]
