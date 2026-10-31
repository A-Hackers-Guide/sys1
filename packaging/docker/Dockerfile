# The sys1 node as a container. Built by GoReleaser (dockers_v2), which
# places each platform's binaries under $TARGETPLATFORM/.
#
#   docker run -d --name sys1 -p 7443:7443 -p 127.0.0.1:7700:7700 \
#     -v sys1-data:/data ghcr.io/a-hackers-guide/sys1
#   docker exec sys1 sys1 open      # prints the dashboard link
FROM alpine:3.20
ARG TARGETPLATFORM
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 sys1 && mkdir /data && chown sys1 /data
COPY $TARGETPLATFORM/sys0 $TARGETPLATFORM/sys1 /usr/bin/
USER sys1
ENV SYS1_DATA_DIR=/data
VOLUME /data
EXPOSE 7443 7700
# The dashboard listens on all interfaces inside the container so a port
# mapping can reach it; publish it on 127.0.0.1 only (see above).
ENTRYPOINT ["sys1"]
CMD ["serve", "--no-open", "--foreground", "--gui", "0.0.0.0:7700"]
