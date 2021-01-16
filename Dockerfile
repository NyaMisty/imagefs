FROM alpine:latest

ADD rootfs/docker-volume-imagefs /
ADD rootfs/loop /

CMD ["/docker-volume-imagefs"]
