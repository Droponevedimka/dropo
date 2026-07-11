FROM debian:stable-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        iproute2 curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY run-tests.sh /run-tests.sh
RUN chmod +x /run-tests.sh
ENTRYPOINT ["/run-tests.sh"]
